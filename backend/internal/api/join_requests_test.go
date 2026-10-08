package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func createJoinRequest(t *testing.T, baseURL, body string) joinRequestDetail {
	t.Helper()

	status, raw := send(t, http.MethodPost, baseURL+"/v1/joinrequests", body, true)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusCreated, raw)
	}

	var got joinRequestDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response %s: %v", raw, err)
	}

	return got
}

func TestHandleCreateJoinRequestReturnsThePendingRequest(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	got := createJoinRequest(t, srv.URL, `{"message":"  Posso entrar?  "}`)

	if got.ID == "" {
		t.Error("ID is empty, want the generated uuid")
	}
	if got.RequesterID != testSessionUserID {
		t.Errorf("RequesterID = %q, want the session account %q", got.RequesterID, testSessionUserID)
	}
	if got.Message != "Posso entrar?" {
		t.Errorf("Message = %q, want the trimmed message", got.Message)
	}
	if got.State != "pending" {
		t.Errorf("State = %q, want %q", got.State, "pending")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is the zero time")
	}
	if got.DecidedBy != nil || got.DecidedAt != nil {
		t.Errorf("a fresh join request carries a decision: %+v", got)
	}
}

func TestHandleCreateJoinRequestAllowsNoMessage(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	got := createJoinRequest(t, srv.URL, `{}`)

	if got.Message != "" {
		t.Errorf("Message = %q, want empty", got.Message)
	}
}

func TestHandleCreateJoinRequestRequiresASession(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, body := send(t, http.MethodPost, srv.URL+"/v1/joinrequests", `{"message":"hi"}`, false)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusUnauthorized, body)
	}
}

func TestHandleCreateJoinRequestRefusesALongMessage(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	body := `{"message":"` + strings.Repeat("é", maxJoinRequestMessage+1) + `"}`

	status, raw := send(t, http.MethodPost, srv.URL+"/v1/joinrequests", body, true)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusUnprocessableEntity, raw)
	}
	assertErrorMessage(t, raw, "message must not be longer than 1000 characters")
}

func TestHandleCreateJoinRequestAcceptsTheLongestMessage(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	createJoinRequest(t, srv.URL, `{"message":"`+strings.Repeat("é", maxJoinRequestMessage)+`"}`)
}

func TestHandleCreateJoinRequestRefusesABadBody(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	for _, body := range []string{``, `null`, `{"message":1}`, `{"msg":"hi"}`} {
		status, raw := send(t, http.MethodPost, srv.URL+"/v1/joinrequests", body, true)
		if status != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want %d — body %s", body, status, http.StatusBadRequest, raw)
		}
	}
}

func TestHandleCreateJoinRequestRefusesASecondPendingRequest(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	createJoinRequest(t, srv.URL, `{"message":"first"}`)

	status, body := send(t, http.MethodPost, srv.URL+"/v1/joinrequests", `{"message":"second"}`, true)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusConflict, body)
	}
	assertErrorMessage(t, body, "you already have a pending join request")
}
