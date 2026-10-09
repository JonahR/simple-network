package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// handleAuthenticate3DS is the bank's ACS endpoint. The network's directory
// server sends it a 3-D Secure authentication request for one of its cards.
func (s *bankServer) handleAuthenticate3DS(w http.ResponseWriter, r *http.Request) {
	if s.mode.Load() == "down" {
		http.Error(w, "issuer down (simulated)", http.StatusServiceUnavailable)
		return
	}
	var req iso8583.ThreeDSRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "invalid 3-D Secure request", http.StatusBadRequest)
		return
	}
	res := s.bank.Authenticate3DS(req)
	log.Printf("%s 3ds authenticate id=%s pan=%s amount=%d status=%s", s.bank.ID, req.ID, card.Mask(req.PAN), req.Amount, res.Status)
	writeJSON(w, http.StatusOK, res)
}

// handleChallenge3DS checks a cardholder's one-time code.
func (s *bankServer) handleChallenge3DS(w http.ResponseWriter, r *http.Request) {
	if s.mode.Load() == "down" {
		http.Error(w, "issuer down (simulated)", http.StatusServiceUnavailable)
		return
	}
	var c iso8583.ThreeDSChallenge
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&c); err != nil || c.ID == "" {
		http.Error(w, "invalid 3-D Secure challenge", http.StatusBadRequest)
		return
	}
	res := s.bank.Challenge3DS(c)
	log.Printf("%s 3ds challenge id=%s status=%s", s.bank.ID, c.ID, res.Status)
	writeJSON(w, http.StatusOK, res)
}
