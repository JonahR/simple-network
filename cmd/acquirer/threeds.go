package main

import (
	"encoding/json"
	"net/http"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// The acquirer is the merchant's 3-D Secure server: the checkout hands it the
// card and amount, and it talks to the network's directory server, which
// routes to the issuer's ACS. Many gateways and acquirers play this role.

func (s *server) handleAuthenticate3DS(w http.ResponseWriter, r *http.Request) {
	var req iso8583.ThreeDSRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.PAN == "" {
		http.Error(w, "invalid 3-D Secure request", http.StatusBadRequest)
		return
	}
	var res iso8583.ThreeDSResult
	if err := s.net.post(r.Context(), "/3ds/authenticate", req, &res); err != nil {
		writeJSON(w, http.StatusOK, iso8583.ThreeDSResult{Status: "U", Reason: "Directory server unavailable: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) handleChallenge3DS(w http.ResponseWriter, r *http.Request) {
	var c iso8583.ThreeDSChallenge
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&c); err != nil || c.ID == "" {
		http.Error(w, "invalid 3-D Secure challenge", http.StatusBadRequest)
		return
	}
	var res iso8583.ThreeDSResult
	if err := s.net.post(r.Context(), "/3ds/challenge", c, &res); err != nil {
		writeJSON(w, http.StatusOK, iso8583.ThreeDSResult{ID: c.ID, Status: "U", Reason: "Directory server unavailable: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}
