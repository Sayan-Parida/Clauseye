package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateClauses(t *testing.T) {
	if err := validateClauses(nil); err == nil {
		t.Fatal("expected empty clause validation error")
	}
	if err := validateClauses([]string{"too short"}); err == nil {
		t.Fatal("expected short clause validation error")
	}
	if err := validateClauses([]string{"This is an ordinary sentence with enough characters."}); err == nil {
		t.Fatal("expected legal-term validation error")
	}
	valid := "The party shall indemnify the client, and this agreement limits liability under the jurisdiction clause."
	if err := validateClauses([]string{valid}); err != nil {
		t.Fatalf("valid clause rejected: %v", err)
	}
}

func TestLayaRequestConstruction(t *testing.T) {
	request := layaRequest{State: "redacted clause", Questions: legalQuestions()}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["state"] != "redacted clause" {
		t.Fatalf("unexpected state: %#v", decoded["state"])
	}
	questions, ok := decoded["questions"].(map[string]any)
	if !ok || len(questions) != 9 {
		t.Fatalf("unexpected question count: %#v", decoded["questions"])
	}
	for name, value := range questions {
		question := value.(map[string]any)
		if question["type"] != "noul" {
			t.Fatalf("%s is not noul: %#v", name, question)
		}
	}
}

func TestValidateLayaResponse(t *testing.T) {
	valid := layaResponse{Answers: map[string]layaAnswer{}}
	for name := range legalQuestions() {
		valid.Answers[name] = layaAnswer{Noul: 0.8, Confidence: 0.9}
	}
	if err := validateLayaResponse(valid); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}

	missing := valid
	delete(missing.Answers, "auto_renewal")
	if err := validateLayaResponse(missing); err == nil {
		t.Fatal("expected missing answer error")
	}

	invalid := valid
	invalid.Answers["uncapped_liability"] = layaAnswer{Noul: 1.1, Confidence: 0.9}
	if err := validateLayaResponse(invalid); err == nil {
		t.Fatal("expected invalid probability error")
	}
}

func TestAggregateResult(t *testing.T) {
	response := layaResponse{Answers: map[string]layaAnswer{}}
	for name := range legalQuestions() {
		response.Answers[name] = layaAnswer{Noul: 0, Confidence: 0.7}
	}
	response.Answers["uncapped_liability"] = layaAnswer{Noul: 0.95, Confidence: 0.94}
	response.Answers["one_sided_indemnity"] = layaAnswer{Noul: 0.9, Confidence: 0.92}
	result := aggregateResult("The party shall indemnify the client without limitation.", 2, response)
	if result.RiskLevel != "high" || result.RiskScore < 0.5 || result.Confidence != 0.94 {
		t.Fatalf("unexpected aggregate result: %#v", result)
	}
	if result.Location == nil || result.Location.ClauseIndex != 2 {
		t.Fatalf("unexpected location: %#v", result.Location)
	}
	found := map[string]bool{}
	for _, item := range result.Issues {
		found[item.Category] = item.Detected
	}
	if !found["uncapped_liability"] || !found["one_sided_indemnity"] {
		t.Fatalf("expected detected issues: %#v", found)
	}
}

func TestAssessBatchMultipleClauses(t *testing.T) {
	calls := 0
	layaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request layaRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		answers := map[string]layaAnswer{}
		for name := range legalQuestions() {
			answers[name] = layaAnswer{Noul: 0, Confidence: 0.8}
		}
		answers["uncapped_liability"] = layaAnswer{Noul: 0.9, Confidence: 0.9}
		_ = json.NewEncoder(w).Encode(layaResponse{Answers: answers})
	}))
	defer layaServer.Close()

	s := &server{client: layaServer.Client(), layaURL: layaServer.URL, batchSize: 2}
	clauses := []string{
		"The party shall indemnify the client under this agreement and liability is unlimited.",
		"The party shall comply with the agreement and jurisdiction clause requirements.",
		"The party shall perform the obligation under this agreement and clause.",
	}
	results, err := s.assessBatch(context.Background(), clauses)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(clauses) || calls != len(clauses) {
		t.Fatalf("unexpected results/calls: %d/%d", len(results), calls)
	}
}

func TestCallLayaTimeoutAndUnavailable(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer slow.Close()
	s := &server{client: slow.Client(), layaURL: slow.URL}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := s.callLaya(ctx, "state"); err == nil {
		t.Fatal("expected timeout error")
	}

	unavailable := &server{client: http.DefaultClient, layaURL: "http://127.0.0.1:1"}
	if _, err := unavailable.callLaya(context.Background(), "state"); err == nil {
		t.Fatal("expected unavailable provider error")
	}
}

func TestVeryLargeClauseValidation(t *testing.T) {
	large := strings.Repeat("The party shall indemnify the client under this agreement and clause. ", 20000)
	if len(large) < 20 {
		t.Fatal("test clause was not large")
	}
	if err := validateClauses([]string{large}); err != nil {
		t.Fatalf("large clause should pass current validation: %v", err)
	}
}
