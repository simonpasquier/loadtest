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
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
)

var (
	help             bool
	concurrent, rate int
)

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
	flag.IntVar(&rate, "rate", 1, "Number of requests per second")
	flag.IntVar(&concurrent, "concurrent", 1, "Maximum number of concurrent request per URI")
	flag.Var(&uris, "uri", "URI to request (can be repeated)")
}

func get(ctx context.Context, u string, ch chan struct{}) {
	client := http.Client{
		Transport: &http.Transport{
			IdleConnTimeout: 1 * time.Minute,
		},
		Timeout: 30 * time.Second,
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			resp, err := client.Get(u)
			if err != nil {
				log.Println(err)
				break
			}
			if resp.StatusCode/100 == 5 {
				log.Printf("%s: got %d status code", u, resp.StatusCode)
			}
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
				get(ctx, uri, ch)
			}(uri)
		}

		wg.Add(1)
		interval := time.Second / time.Duration(rate)
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

	log.Println("Initialization completed")
	s := make(chan os.Signal, 1)
	signal.Notify(s, os.Interrupt)
	// Block until a signal is received.
	<-s
	log.Println("Shutting down")
	cancel()
	wg.Wait()
}
