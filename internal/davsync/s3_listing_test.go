package davsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

func TestS3UsageListingMustFinish(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pages  int
		broken string
	}{
		{"one page", 1, ""},
		{"multiple pages", 3, ""},
		{"last allowed page", 100, ""},
		{"missing next token", 1, "missing"},
		{"repeated next token", 2, "repeat"},
		{"token cycle", 3, "cycle"},
		{"page limit", 100, "limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page := int(requests.Add(1))
				q := r.URL.Query()
				if r.Method != http.MethodGet || q.Get("list-type") != "2" {
					t.Errorf("unexpected listing request: %s %s", r.Method, r.URL)
				}
				if tc.broken == "" && page > 1 && q.Get("continuation-token") != strconv.Itoa(page-1) {
					t.Errorf("page %d got continuation token %q", page, q.Get("continuation-token"))
				}
				next := strconv.Itoa(page)
				switch tc.broken {
				case "missing":
					next = ""
				case "repeat":
					next = "1"
				case "cycle":
					if page == 3 {
						next = "1"
					}
				}
				truncated := tc.broken != "" || page < tc.pages
				fmt.Fprintf(w, `<ListBucketResult><Contents><Key>%s%d.magpie-usage</Key><ETag>etag-%d</ETag></Contents><IsTruncated>%t</IsTruncated><NextContinuationToken>%s</NextContinuationToken></ListBucketResult>`, q.Get("prefix"), page, page, truncated, next)
			}))
			defer srv.Close()
			s, err := newS3(Config{URL: "s3://test-bucket", Endpoint: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.list(context.Background())
			if tc.broken != "" {
				if err == nil || got != nil {
					t.Errorf("incomplete listing returned %d files, error %v", len(got), err)
				}
			} else if err != nil || len(got) != tc.pages {
				t.Errorf("complete listing returned %d files, error %v; want %d", len(got), err, tc.pages)
			}
			if got := int(requests.Load()); got != tc.pages {
				t.Errorf("made %d requests, want %d", got, tc.pages)
			}
		})
	}
}

func TestS3IncompleteListingKeepsSharedUsage(t *testing.T) {
	newComputer(t).use(t)
	day := time.Now().Local().Format(time.DateOnly)
	const other = "0123456789abcdef"
	shared := usage.SharedDay{Computer: other, Day: day, Name: "other computer", Calls: []usage.SharedCall{{Record: call(time.Now(), "example-model", 100)}}}
	if err := usage.KeepShared(shared); err != nil {
		t.Fatal(err)
	}
	filename := other + "-" + day + usageExt
	st := &usageState{Got: map[string]string{filename: "known-etag"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>`)
		}
	}))
	defer srv.Close()
	cfg := Config{URL: "s3://test-bucket", Endpoint: srv.URL, Passphrase: "test passphrase"}
	s, err := newS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := shareWith(context.Background(), cfg, st, s); err == nil {
		t.Error("sharing with an incomplete listing succeeded")
	}
	if !slices.Contains(usage.SharedKept(), other+"/"+day) {
		t.Error("incomplete listing deleted the other computer's cached day")
	}
	if st.Got[filename] != "known-etag" {
		t.Error("incomplete listing discarded the cached version")
	}
}
