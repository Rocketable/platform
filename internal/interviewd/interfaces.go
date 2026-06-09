package interviewd

import (
	"strconv"

	"github.com/Rocketable/platform/internal/netutil"
)

func servingAddresses(port int, id string) []string {
	return netutil.CandidateHTTPURLs(netutil.URLCandidatesOptions{
		Addr:               ":" + strconv.Itoa(port),
		Path:               id,
		IncludePublic:      true,
		IncludeUnspecified: true,
	})
}
