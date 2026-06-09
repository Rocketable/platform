package quickweb

import "github.com/Rocketable/platform/internal/netutil"

func candidateURLs(addr, baseURL string) []string {
	return netutil.CandidateHTTPURLs(netutil.URLCandidatesOptions{Addr: addr, BaseURL: baseURL})
}
