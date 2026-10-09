package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/network"
)

func TestReverseEndpoint(t *testing.T) {
	sw := &network.Switch{BINs: network.DefaultBINs(), Recorder: network.NewRecorder(10), Now: time.Now}
	defer sw.Close()
	s := &server{sw: sw}

	// An 0420 for an authorization the network has not seen is acknowledged.
	body := `{"mti":"0420","de2_pan":"4242424242424242","de4_amount":100,"de7_transmission_datetime":"1009143005",
		"de11_stan":"000002","de32_acquirer_id":"100001","de37_rrn":"628214000001","de39_response_code":"68",
		"de90_original_data":{"mti":"0100","stan":"000001","transmission_time":"1009143000","acquirer_id":"100001"}}`
	w := httptest.NewRecorder()
	s.handleReverse(w, httptest.NewRequest(http.MethodPost, "/reverse", strings.NewReader(body)))
	var ack iso8583.ReversalResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &ack) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if ack.MTI != "0430" || ack.STAN != "000002" || ack.ResponseCode != "00" || ack.Matched {
		t.Errorf("%+v", ack)
	}

	w = httptest.NewRecorder()
	s.handleReverse(w, httptest.NewRequest(http.MethodPost, "/reverse", strings.NewReader(`{"mti":"0420"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid 0420: %d", w.Code)
	}
}
