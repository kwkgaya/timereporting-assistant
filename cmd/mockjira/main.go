// Command mockjira runs the in-memory mock Jira server used to test the
// worklog write flow without touching a real Jira instance.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/kwkgaya/timereporting-assistant/internal/mockjira"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	port := flag.Int("port", 9099, "port to listen on")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("mockjira %s\n", version)
		return
	}

	srv := mockjira.NewDefault()
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("mock Jira listening on http://localhost%s (inspect page at /)", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
