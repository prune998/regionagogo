package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/akhenakh/regionagogo"
	"github.com/akhenakh/regionagogo/db/boltdb"
	pb "github.com/akhenakh/regionagogo/regionagogosvc"
	kitlog "github.com/go-kit/kit/log"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/namsral/flag"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

var (
	// version is filled by -ldflags  at compile time
	version        = "no version set"
	displayVersion = flag.Bool("version", false, "Show version and quit")
	dbpath         = flag.String("dbpath", "", "Database path")
	debug          = flag.Bool("debug", false, "Enable debug")
	httpPort       = flag.Int("httpPort", 8082, "http debug port to listen on")
	grpcPort       = flag.Int("grpcPort", 8083, "grpc port to listen on")
	cachedEntries  = flag.Uint("cachedEntries", 0, "Region Cache size, 0 for disabled")
)

type server struct {
	regionagogo.GeoFenceDB
}

func (s *server) GetRegion(ctx context.Context, p *pb.Point) (*pb.RegionResponse, error) {

	queryStartTime := time.Now()
	region, err := s.StubbingQuery(float64(p.Latitude), float64(p.Longitude))
	if err != nil {
		return nil, err
	}

	// update Prom Histogram
	promQueryProcessedDelay.Observe(time.Since(queryStartTime).Seconds())

	if region == nil || len(region) == 0 {
		return &pb.RegionResponse{Code: "unknown"}, nil
	}

	// default is to lookup for "iso"
	iso, ok := region[0].Data["iso"]
	if !ok {
		return &pb.RegionResponse{Code: "unknown"}, nil
	}

	rs := pb.RegionResponse{Code: iso}
	return &rs, nil
}

// queryHandler takes a lat & lng query params and return a JSON
// with the country of the coordinate
func (s *server) queryHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	slat := query.Get("lat")
	lat, err := strconv.ParseFloat(slat, 64)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	slng := query.Get("lng")
	lng, err := strconv.ParseFloat(slng, 64)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	queryStartTime := time.Now()
	fences, err := s.StubbingQuery(lat, lng)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// update Prom Histogram
	promQueryProcessedDelay.Observe(time.Since(queryStartTime).Seconds())

	w.Header().Set("Content-Type", "application/json")

	if len(fences) < 1 {
		js, _ := json.Marshal(map[string]string{"name": "unknown"})
		w.Write(js)
		return
	}

	js, _ := json.Marshal(fences[0].Data)
	w.Write(js)
}

func main() {
	flag.Parse()

	if *displayVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	var logger kitlog.Logger
	logger = kitlog.NewJSONLogger(kitlog.NewSyncWriter(os.Stdout))

	opts := []boltdb.GeoFenceBoltDBOption{
		boltdb.WithCachedEntries(*cachedEntries),
		boltdb.WithDebug(*debug),
	}
	gs, err := boltdb.NewGeoFenceBoltDB(*dbpath, opts...)
	if err != nil {
		panic(err)
	}

	s := &server{GeoFenceDB: gs}
	http.HandleFunc("/query", s.queryHandler)

	// prometheus metrics
	http.Handle("/metrics", promhttp.Handler())

	// healthz basic
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		m := map[string]interface{}{"version": version, "status": "OK"}

		b, err := json.Marshal(m)
		if err != nil {
			http.Error(w, "no valid point for this device_id", 500)
			return
		}

		w.Write(b)
	})

	// start HTTP listener
	go func() {
		logger.Log("msg", fmt.Sprintf("listening HTTP (metrics & API) on %d", *httpPort))
		logger.Log("err", http.ListenAndServe(fmt.Sprintf(":%d", *httpPort), nil))
	}()

	// start GRPC listener
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *grpcPort))
	if err != nil {
		logger.Log("msg", fmt.Sprintf("failed to listen on %d: %v", *grpcPort), "err", err)
		os.Exit(2)
	}

	// enable Histogram stats in /metrics
	grpc_prometheus.EnableHandlingTimeHistogram()

	grpcServer := grpc.NewServer(
		grpc.StreamInterceptor(grpc_prometheus.StreamServerInterceptor),
		grpc.UnaryInterceptor(grpc_prometheus.UnaryServerInterceptor),
	)
	pb.RegisterRegionAGogoServer(grpcServer, s)
	grpcServer.Serve(lis)
}
