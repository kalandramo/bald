package webrtc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pion/webrtc/v4"
)

const hostCandidate = "candidate:1 1 udp 2130706431 127.0.0.1 54321 typ host"

func newTestSession(t *testing.T, withRemoteDescription bool) (*Server, *Session) {
	t.Helper()

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection() error = %v", err)
	}

	if withRemoteDescription {
		offerer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatalf("NewPeerConnection() for offerer error = %v", err)
		}
		t.Cleanup(func() { _ = offerer.Close() })

		if _, err = offerer.CreateDataChannel("wind", nil); err != nil {
			t.Fatalf("CreateDataChannel() error = %v", err)
		}
		offer, err := offerer.CreateOffer(nil)
		if err != nil {
			t.Fatalf("CreateOffer() error = %v", err)
		}
		if err = offerer.SetLocalDescription(offer); err != nil {
			t.Fatalf("SetLocalDescription() error = %v", err)
		}
		<-webrtc.GatheringCompletePromise(offerer)

		if err = pc.SetRemoteDescription(offer); err != nil {
			t.Fatalf("SetRemoteDescription() error = %v", err)
		}
	}

	srv := NewServer(WithAddress("127.0.0.1:0"))
	session := NewSession(srv, pc, nil)
	srv.trackSignalSession(session)

	t.Cleanup(func() {
		session.Close()
		_ = srv.Stop(t.Context())
	})

	return srv, session
}

func TestHandleICECandidate_AppliesToSession(t *testing.T) {
	srv, session := newTestSession(t, true)

	rr := httptest.NewRecorder()
	srv.handleICECandidate(session.SessionID(), &webrtc.ICECandidateInit{Candidate: hostCandidate}, rr)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp signalResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	if resp.SessionID != session.SessionID() {
		t.Fatalf("unexpected session id: got %q want %q", resp.SessionID, session.SessionID())
	}
	if resp.MessageType != "candidate" {
		t.Fatalf("unexpected message type: %q", resp.MessageType)
	}

	session.Close()
	if got := srv.getSignalSession(session.SessionID()); got != nil {
		t.Fatal("session should be removed from signal registry after close")
	}
}

func TestHandleICECandidate_RequiresSessionID(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))

	rr := httptest.NewRecorder()
	srv.handleICECandidate("", &webrtc.ICECandidateInit{Candidate: hostCandidate}, rr)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if rr.Body.Len() == 0 {
		t.Fatal("expected error body")
	}
}

func TestHandleICECandidate_UnknownSession(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))

	rr := httptest.NewRecorder()
	srv.handleICECandidate("missing", &webrtc.ICECandidateInit{Candidate: hostCandidate}, rr)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestHandleICECandidate_ClosedSession(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	session := &Session{id: "closed"}
	srv.trackSignalSession(session)

	rr := httptest.NewRecorder()
	srv.handleICECandidate(session.SessionID(), &webrtc.ICECandidateInit{Candidate: hostCandidate}, rr)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestHandleICECandidate_NoRemoteDescription(t *testing.T) {
	srv, session := newTestSession(t, false)

	rr := httptest.NewRecorder()
	srv.handleICECandidate(session.SessionID(), &webrtc.ICECandidateInit{Candidate: hostCandidate}, rr)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSignalHandler_OfferThenCandidate(t *testing.T) {
	srv := NewServer(
		WithAddress("127.0.0.1:0"),
		WithPath("/signal"),
	)

	offerer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection() error = %v", err)
	}
	defer offerer.Close()

	if _, err = offerer.CreateDataChannel("wind", nil); err != nil {
		t.Fatalf("CreateDataChannel() error = %v", err)
	}
	offer, err := offerer.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer() error = %v", err)
	}
	if err = offerer.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription() error = %v", err)
	}
	<-webrtc.GatheringCompletePromise(offerer)

	offerBody, err := json.Marshal(signalRequest{Offer: offerer.LocalDescription()})
	if err != nil {
		t.Fatalf("marshal offer error = %v", err)
	}

	offerRR := httptest.NewRecorder()
	srv.signalHandler(offerRR, httptest.NewRequest(http.MethodPost, "/signal", bytes.NewReader(offerBody)))

	if offerRR.Code != http.StatusOK {
		t.Fatalf("offer request expected 200, got %d: %s", offerRR.Code, offerRR.Body.String())
	}

	var answer signalResponse
	if err = json.Unmarshal(offerRR.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode answer error = %v", err)
	}
	if answer.SessionID == "" {
		t.Fatal("expected session_id in answer")
	}
	if answer.Answer == nil || answer.Answer.Type != webrtc.SDPTypeAnswer || answer.Answer.SDP == "" {
		t.Fatalf("expected answer in response, got %+v", answer.Answer)
	}

	session := srv.getSignalSession(answer.SessionID)
	if session == nil {
		t.Fatal("session should be tracked before data channel opens")
	}
	defer session.Close()

	candidateBody, err := json.Marshal(signalRequest{
		SessionID: answer.SessionID,
		Candidate: &webrtc.ICECandidateInit{Candidate: hostCandidate, SDPMid: ptr("0"), SDPMLineIndex: ptr(uint16(0))},
	})
	if err != nil {
		t.Fatalf("marshal candidate error = %v", err)
	}

	candidateRR := httptest.NewRecorder()
	srv.signalHandler(candidateRR, httptest.NewRequest(http.MethodPost, "/signal", bytes.NewReader(candidateBody)))

	if candidateRR.Code != http.StatusOK {
		t.Fatalf("candidate request expected 200, got %d: %s", candidateRR.Code, candidateRR.Body.String())
	}
}

func ptr[T any](v T) *T {
	return &v
}
