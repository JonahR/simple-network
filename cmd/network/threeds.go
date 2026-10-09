package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// handleAuthenticate3DS is the network's 3-D Secure directory server. A
// merchant's checkout sends it the card and amount; it routes the request to
// the issuer's ACS by BIN.
func (s *server) handleAuthenticate3DS(w http.ResponseWriter, r *http.Request) {
	var req iso8583.ThreeDSRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.PAN == "" {
		http.Error(w, "invalid 3-D Secure request", http.StatusBadRequest)
		return
	}
	res := s.sw.Authenticate3DS(r.Context(), req)
	log.Printf("3ds authenticate id=%s pan=%s amount=%d status=%s", res.ID, card.Mask(req.PAN), req.Amount, res.Status)
	writeJSON(w, http.StatusOK, res)
}

// handleChallenge3DS forwards the cardholder's one-time code to the ACS.
func (s *server) handleChallenge3DS(w http.ResponseWriter, r *http.Request) {
	var c iso8583.ThreeDSChallenge
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&c); err != nil || c.ID == "" {
		http.Error(w, "invalid 3-D Secure challenge", http.StatusBadRequest)
		return
	}
	res := s.sw.Challenge3DS(r.Context(), c)
	log.Printf("3ds challenge id=%s status=%s", c.ID, res.Status)
	writeJSON(w, http.StatusOK, res)
}
