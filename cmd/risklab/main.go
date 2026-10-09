// Command risklab runs the Risk lab: a page that stages fraud attempts and
// failures against the running simulation and shows which control stopped
// each one, and where no control did.
//
// Every drill goes through the real services. Card-present and card-not-present
// probes enter as a terminal of the "Risk Lab" merchant at the acquirer, so
// they appear on the acquirer, network, issuer, and settlement pages like any
// other sale and can be followed there. A few probes go straight to the
// network, the way a rogue system that bypasses an acquirer would. Failure
// drills switch an issuer's simulated fault mode and switch it back.
//
// A drill undoes what it did: it voids its own approvals, unfreezes cards,
// and restores issuer modes, so it doesn't leave holds that would be cleared.
package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/learn"
	"github.com/JonahR/simple-network/internal/ui"
)

//go:embed web
var webFS embed.FS

func main() {
	addr := env("RISKLAB_ADDR", ":8093")
	pinKey, err := demokeys.Load("ACQUIRER_PIN_KEY", demokeys.AcquirerPIN)
	if err != nil {
		log.Fatal(err)
	}
	cfg := Config{
		AcquirerURL: env("ACQUIRER_URL", "http://localhost:8081"),
		NetworkURL:  env("NETWORK_URL", "http://localhost:8090"),
		IssuerURLs: map[string]string{
			"FSB": env("ISSUER_FSB_URL", "http://localhost:8091"),
			"UCB": env("ISSUER_UCB_URL", "http://localhost:8092"),
		},
		AcquirerID: env("ACQUIRER_ID", "100001"),
		MerchantID: env("RISKLAB_MERCHANT_ID", "000000000099001"),
		TerminalID: env("RISKLAB_TERMINAL_ID", "RISK0001"),
		PINKey:     pinKey,
		Links: Links{
			Acquirer:   env("ACQUIRER_PUBLIC_URL", "http://localhost:8081"),
			Network:    env("NETWORK_DASHBOARD_URL", "http://localhost:8090"),
			Settlement: env("SETTLEMENT_PUBLIC_URL", "http://localhost:8090/settlement/"),
			Issuers: map[string]string{
				"FSB": env("ISSUER_FSB_PUBLIC_URL", "http://localhost:8091"),
				"UCB": env("ISSUER_UCB_PUBLIC_URL", "http://localhost:8092"),
			},
		},
	}
	lab := NewLab(cfg)

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	lab.Register(mux)
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	ui.Register(mux, ui.PagesFromEnv())
	mux.Handle("GET /learn/", learn.Handler(learn.Links{
		POS:       env("POS_PUBLIC_URL", "http://localhost:8080"),
		Dashboard: env("NETWORK_DASHBOARD_URL", "http://localhost:8090"),
	}))

	// On SIGINT or SIGTERM, stop the running drill so it can clean up.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: addr, Handler: mux}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("risk lab listening on %s (merchant %s, terminal %s, acquirer %s)", addr, cfg.MerchantID, cfg.TerminalID, cfg.AcquirerURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-drained
	lab.Close()
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
