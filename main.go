// Copyright 2018 Simon Pasquier
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mwitkow/go-conntrack"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const reqTimeout = 5 * time.Second

var (
	help       bool
	concurrent int
	rate       float64
	listen     string

	duration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "loadtest_http_requests_duration_seconds",
			Help: "Histogram of latencies for HTTP requests.",
		},
		[]string{"uri", "code"},
	)
)

func init() {
	prometheus.MustRegister(duration)
}

type uriSlice []string

func (u *uriSlice) String() string {
	return strings.Join(*u, ",")
}

func (u *uriSlice) Set(v string) error {
	*u = append(*u, v)
	return nil
}

var uris uriSlice

func init() {
	flag.BoolVar(&help, "help", false, "Help message")
	flag.Float64Var(&rate, "rate", 1.0, "Number of requests per second")
	flag.IntVar(&concurrent, "concurrent", 1, "Maximum number of concurrent request per URI")
	flag.Var(&uris, "uri", "URI to request (can be repeated)")
	flag.StringVar(&listen, "listen", "", "Listen address")
}

func get(ctx context.Context, u string, ch chan struct{}, obs prometheus.ObserverVec) {
	client := http.Client{
		Transport: &http.Transport{
			IdleConnTimeout:     1 * time.Minute,
			TLSHandshakeTimeout: reqTimeout,
			DialContext: conntrack.NewDialContextFunc(
				conntrack.DialWithTracing(),
				conntrack.DialWithName(u),
			),
		},
		Timeout: reqTimeout,
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			req, err := http.NewRequest("GET", u, nil)
			req = req.WithContext(ctx)
			if err != nil {
				log.Println(err)
				break
			}
			start := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				log.Println(err)
				break
			}
			obs.WithLabelValues(strconv.Itoa(resp.StatusCode)).Observe(time.Since(start).Seconds())
			if resp.StatusCode/100 == 5 {
				log.Printf("%s: got %d status code", u, resp.StatusCode)
			}
			// Consume body to re-use the connection.
			//nolint: errcheck
			io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

func main() {
	flag.Parse()
	if help {
		fmt.Fprintln(os.Stderr, "Simple HTTP load tester")
		flag.PrintDefaults()
		os.Exit(0)
	}

	if len(uris) == 0 {
		fmt.Fprintln(os.Stderr, "Missing --uri parameter.")
		flag.PrintDefaults()
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, uri := range uris {
		ch := make(chan struct{}, concurrent)
		for i := 0; i < concurrent; i++ {
			wg.Add(1)
			go func(uri string) {
				defer wg.Done()
				get(ctx, uri, ch, duration.MustCurryWith(map[string]string{"uri": uri}))
			}(uri)
		}

		wg.Add(1)
		interval := time.Duration(float64(time.Second) / rate)
		go func() {
			defer wg.Done()
			for {
				// Randomize a bit the delay between requests.
				d := (1.5 - rand.Float64()) * float64(interval)
				tick := time.NewTicker(time.Duration(d))
				select {
				case <-ctx.Done():
					tick.Stop()
					return
				case <-tick.C:
					tick.Stop()
					select {
					case ch <- struct{}{}:
					default:
						log.Printf("Channel full")
					}
				}
			}
		}()
	}

	if listen != "" {
		wg.Add(1)
		srv := &http.Server{Addr: listen}
		go func() {
			defer wg.Done()
			errc := make(chan error)
			go func() {
				http.HandleFunc("/metrics", promhttp.Handler().ServeHTTP)
				log.Println("Listening on", listen)
				errc <- srv.ListenAndServe()

			}()
			select {
			case <-ctx.Done():
				srv.Close()
			case err := <-errc:
				log.Println("Error when starting server:", err)
			}
		}()
	}

	log.Println("Initialization completed")
	s := make(chan os.Signal, 1)
	signal.Notify(s, os.Interrupt)
	// Block until a signal is received.
	<-s
	log.Println("Shutting down")
	cancel()
	wg.Wait()
}
