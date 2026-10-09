package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Vidal322/activity-library/internal/store"
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

func listPendingJoinRequests(t *testing.T, baseURL string) (string, pendingJoinRequestsResponse) {
	t.Helper()

	status, raw := send(t, http.MethodGet, baseURL+"/v1/joinrequests", ``, true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusOK, raw)
	}

	var got pendingJoinRequestsResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response %s: %v", raw, err)
	}

	return string(raw), got
}

func TestHandleListPendingJoinRequestsReturnsPendingRequests(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	created := createJoinRequest(t, baseURL, `{"message":"Posso entrar?"}`)

	_, got := listPendingJoinRequests(t, baseURL)

	if len(got.JoinRequests) != 1 {
		t.Fatalf("got %d join requests, want 1: %+v", len(got.JoinRequests), got)
	}

	jr := got.JoinRequests[0]
	if jr.ID != created.ID {
		t.Errorf("ID = %q, want %q", jr.ID, created.ID)
	}
	if jr.RequesterID != testSessionUserID {
		t.Errorf("RequesterID = %q, want %q", jr.RequesterID, testSessionUserID)
	}
	if jr.RequesterName != "Session User" {
		t.Errorf("RequesterName = %q, want %q", jr.RequesterName, "Session User")
	}
	if jr.RequesterEmail != sessionUserEmail {
		t.Errorf("RequesterEmail = %q, want %q", jr.RequesterEmail, sessionUserEmail)
	}
	if jr.Message != "Posso entrar?" || jr.State != "pending" {
		t.Errorf("join request fields not returned: %+v", jr)
	}
}

func TestHandleListPendingJoinRequestsIsAnEmptyArray(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	raw, _ := listPendingJoinRequests(t, baseURL)

	if strings.TrimSpace(raw) != `{"join_requests":[]}` {
		t.Errorf("body = %s, want an empty join_requests array", raw)
	}
}

func TestHandleListPendingJoinRequestsIsForAdmins(t *testing.T) {
	srv, _ := newAuthedTestServer(t)

	status, body := send(t, http.MethodGet, srv.URL+"/v1/joinrequests", ``, true)
	if status != http.StatusForbidden {
		t.Fatalf("outsider: status = %d, want %d — body %s", status, http.StatusForbidden, body)
	}
	assertErrorMessage(t, body, msgNotAdmin)

	status, body = send(t, http.MethodGet, srv.URL+"/v1/joinrequests", ``, false)
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want %d — body %s", status, http.StatusUnauthorized, body)
	}
}

func seedOutsiderJoinRequest(t *testing.T, pool *pgxpool.Pool) (string, string) {
	t.Helper()

	ctx := testContext(t)

	var outsiderID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (name, email, pass_hash)
		 VALUES ('Rui Costa', 'rui@example.test', 'not-a-real-hash')
		 RETURNING id`,
	).Scan(&outsiderID); err != nil {
		t.Fatalf("could not seed the outsider: %v", err)
	}

	joinReq, err := store.CreateJoinRequest(ctx, pool, outsiderID, "Posso entrar?")
	if err != nil {
		t.Fatalf("could not seed the join request: %v", err)
	}

	return outsiderID, joinReq.ID
}

func TestHandleAcceptJoinRequestApprovesAndPromotes(t *testing.T) {
	baseURL, pool := newAdminTestServer(t)

	outsiderID, joinReqID := seedOutsiderJoinRequest(t, pool)

	status, raw := send(t, http.MethodPost, baseURL+"/v1/joinrequests/"+joinReqID+"/accept", ``, true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d — body %s", status, http.StatusOK, raw)
	}

	var got joinRequestDetail
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("could not decode the response %s: %v", raw, err)
	}

	if got.ID != joinReqID {
		t.Errorf("ID = %q, want %q", got.ID, joinReqID)
	}
	if got.State != "approved" {
		t.Errorf("State = %q, want %q", got.State, "approved")
	}
	if got.DecidedBy == nil || *got.DecidedBy != testSessionUserID {
		t.Errorf("DecidedBy = %v, want the session account %q", got.DecidedBy, testSessionUserID)
	}
	if got.DecidedAt == nil {
		t.Error("DecidedAt is nil, want the decision time")
	}

	var role string
	if err := pool.QueryRow(testContext(t),
		`SELECT role FROM users WHERE id = $1`, outsiderID,
	).Scan(&role); err != nil {
		t.Fatalf("could not read the requester back: %v", err)
	}
	if role != "member" {
		t.Errorf("requester role = %q, want %q", role, "member")
	}
}

func TestHandleAcceptJoinRequestOnlyDecidesOnce(t *testing.T) {
	baseURL, pool := newAdminTestServer(t)

	_, joinReqID := seedOutsiderJoinRequest(t, pool)

	status, raw := send(t, http.MethodPost, baseURL+"/v1/joinrequests/"+joinReqID+"/accept", ``, true)
	if status != http.StatusOK {
		t.Fatalf("first: status = %d, want %d — body %s", status, http.StatusOK, raw)
	}

	status, raw = send(t, http.MethodPost, baseURL+"/v1/joinrequests/"+joinReqID+"/accept", ``, true)
	if status != http.StatusNotFound {
		t.Fatalf("second: status = %d, want %d — body %s", status, http.StatusNotFound, raw)
	}
}

func TestHandleAcceptJoinRequestRefusesABadID(t *testing.T) {
	baseURL, _ := newAdminTestServer(t)

	status, raw := send(t, http.MethodPost, baseURL+"/v1/joinrequests/not-a-uuid/accept", ``, true)
	if status != http.StatusBadRequest {
		t.Errorf("malformed: status = %d, want %d — body %s", status, http.StatusBadRequest, raw)
	}

	status, raw = send(t, http.MethodPost, baseURL+"/v1/joinrequests/10000000-0000-7000-8000-0000000000aa/accept", ``, true)
	if status != http.StatusNotFound {
		t.Errorf("unknown: status = %d, want %d — body %s", status, http.StatusNotFound, raw)
	}
}

func TestHandleAcceptJoinRequestIsForAdmins(t *testing.T) {
	srv, pool := newAuthedTestServer(t)

	outsiderID, joinReqID := seedOutsiderJoinRequest(t, pool)

	status, body := send(t, http.MethodPost, srv.URL+"/v1/joinrequests/"+joinReqID+"/accept", ``, true)
	if status != http.StatusForbidden {
		t.Fatalf("outsider: status = %d, want %d — body %s", status, http.StatusForbidden, body)
	}
	assertErrorMessage(t, body, msgNotAdmin)

	status, body = send(t, http.MethodPost, srv.URL+"/v1/joinrequests/"+joinReqID+"/accept", ``, false)
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want %d — body %s", status, http.StatusUnauthorized, body)
	}

	var role string
	if err := pool.QueryRow(testContext(t),
		`SELECT role FROM users WHERE id = $1`, outsiderID,
	).Scan(&role); err != nil {
		t.Fatalf("could not read the requester back: %v", err)
	}
	if role != "outsider" {
		t.Errorf("requester role = %q, want %q", role, "outsider")
	}
}
