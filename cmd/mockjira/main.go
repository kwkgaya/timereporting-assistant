// Command mockjira runs the in-memory mock Jira server used to test the
// worklog write flow without touching a real Jira instance.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/kwkgaya/timeporting-assistant/internal/mockjira"
)

func main() {
	port := flag.Int("port", 8099, "port to listen on")
	flag.Parse()

	srv := mockjira.NewDefault()
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("mock Jira listening on http://localhost%s (inspect page at /)", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
