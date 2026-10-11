// Command mock serves the real API over in-memory stores seeded with fake
// data, for the frontend to build against. No database, no SMS, no secrets.
//
// It is the production router, handlers and authorization rules: only the
// storage is fake. Status codes, error bodies and the consent and binding
// rules therefore match the real service, because they are the real service.
// State lives in memory and is reset by a restart.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/api"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/consent"
	"github.com/proleaf/proleaf-api/internal/enroll"
	"github.com/proleaf/proleaf-api/internal/stats"
	"github.com/proleaf/proleaf-api/internal/testsupport"
)

const (
	otpCode       = "123456"
	policyVersion = "2026-01-01"

	adminPhone   = "+8613800000001"
	teacherPhone = "+8613800000002"
	parentPhone  = "+8613800000003"
)

// enrollmentCodes are unused codes a fresh parent account can bind with.
var enrollmentCodes = []string{"MOCK-CODE-0001", "MOCK-CODE-0002"}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	h := newMock()
	fmt.Printf(`ProLeaf mock API on %s%s  (spec: %s/openapi.yaml)

  Sign in with any phone below; the OTP is always %s.
    admin    %s
    teacher  %s   two classes, eight weeks of assessments
    parent   %s   two children; "comparison" consent declined for the second
    any other number signs in as a new parent with no children
  Enrollment codes for a new parent: %v
  Policy version: %s
  Data is in memory. Restart to reset it.

`, *addr, api.Version, api.Version, otpCode, adminPhone, teacherPhone, parentPhone, enrollmentCodes, policyVersion)

	log.Fatal(http.ListenAndServe(*addr, cors(h.Handler)))
}

func newMock() *testsupport.Harness {
	h := testsupport.NewHarnessWith(testsupport.Options{PolicyVersion: policyVersion, FixedOTP: otpCode})
	seed(h.W)
	return h
}

// cors is wide open, so a Flutter web build or a Swagger UI on another origin
// can call the mock. The real service sets no CORS headers.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func seed(w *testsupport.World) {
	account := func(role auth.Role, phone, name string) auth.Account {
		a := w.AddAccount(role, phone)
		a.DisplayName = name
		w.Accounts[a.ID] = a
		return a
	}
	account(auth.RoleAdmin, adminPhone, "教务处 周主任")
	teacher := account(auth.RoleTeacher, teacherPhone, "李老师")
	parent := account(auth.RoleParent, parentPhone, "")

	sc, _ := testsupport.AdminStore{W: w}.CreateSchool(context.Background(), "浦东实验小学")

	// A fixed seed: the same scores on every start, so a screenshot taken
	// today still matches the data tomorrow.
	rng := rand.New(rand.NewSource(20261003))
	today := time.Now().UTC().Truncate(24 * time.Hour)

	var firstOfEach []uuid.UUID
	var blue []uuid.UUID
	for _, c := range []struct {
		name     string
		stage    int
		birth    int
		students []string
	}{
		{"CAT4 Blue", 4, 2016, []string{"王梓涵", "李浩然", "张雨桐", "刘子轩", "陈思琪", "杨一诺", "赵宇航", "黄欣怡"}},
		{"CAT3 Green", 3, 2017, []string{"周子墨", "吴梦洁", "徐俊熙", "孙诗涵", "马天佑", "朱可馨"}},
	} {
		class := w.AddClass(c.name, c.stage)
		class.SchoolID = &sc.ID
		w.Classes[class.ID] = class
		w.Assign(class.ID, teacher.ID)

		for i, name := range c.students {
			dob := time.Date(c.birth, time.Month(1+rng.Intn(12)), 1+rng.Intn(28), 0, 0, 0, 0, time.UTC)
			st := w.AddStudent(class.ID, name, c.stage, dob)
			if i == 0 {
				firstOfEach = append(firstOfEach, st.ID)
			}
			if c.stage == 4 {
				blue = append(blue, st.ID)
			}

			// One session a week for eight weeks, around a level of the
			// student's own, drifting upward a little.
			level := 62 + rng.Float64()*28
			for week := 7; week >= 0; week-- {
				score := level + float64(7-week)*0.6 + rng.NormFloat64()*5
				score = float64(int(min(max(score, 35), 99)*2)) / 2 // halves
				w.AddAssessment(st.ID, teacher.ID, today.AddDate(0, 0, -7*week), score)
				participation := 2 + rng.Intn(4)
				last := &w.Assessments[len(w.Assessments)-1]
				last.Participation = &participation
				last.Note = ""
				if rng.Intn(5) == 0 {
					last.Note = "课堂发言积极，继续保持。"
				}
			}
		}
		for months := 0; months <= 2; months++ {
			w.ComputeStats(class.ID, stats.Month(today).AddDate(0, -months, 0), 5)
		}
	}

	// The seeded parent: fully consented for the first child, and progress
	// only for the second, so the consent_required path has something to show.
	for _, child := range firstOfEach {
		w.Bind(parent.ID, child)
		w.GrantConsent(parent.ID, child, policyVersion)
	}
	w.WithdrawConsent(parent.ID, firstOfEach[1], consent.ScopeComparison, policyVersion)

	for i, code := range enrollmentCodes {
		hash, err := enroll.HashRaw(testsupport.CodePepper, code)
		if err != nil {
			panic(err)
		}
		w.AddCode(hash, blue[i+1], today.AddDate(0, 1, 0))
	}
}
