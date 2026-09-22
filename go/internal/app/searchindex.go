//go:build linux

// The optional name index, and what building one would cost.
//
// The index is an escalation, not the default. Search works by walking, and
// an index is what an operator adds once measurement shows the walk is too
// slow for their corpus. Sizing it before building it is the point of the
// estimate: the build traverses everything, and an operator deciding whether
// to spend that wants the number first.
package app

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/search/stowcloud"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	search "github.com/stowcloud/namesearch"
)

// adminIndexEstimate measures the corpus and reports what an index would cost.
//
// Every share is measured, not the caller's own view. The index covers the
// whole deployment, so a figure taken from one account's shares would be
// smaller than the index that gets built.
func (e *Engine) adminIndexEstimate(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}

	sources := indexSourcesOf(e.Core.ScanSources())
	result, err := search.ScanCorpus(c.Request.Context(), sources, search.ScanOptions{})
	if err != nil {
		failKnown(c, err)
		return
	}

	// A measurement from this deployment's own disk and corpus beats the
	// compiled-in guess; an unreadable rate falls back to it the same way an
	// unset one does.
	rate, rerr := e.State.IndexBuildRate(c.Request.Context())
	if rerr != nil {
		rate = 0
	}
	estimate := search.EstimateNameIndex(result.Stats, indexBlockSize, rate)
	writeJSON(c, http.StatusOK, handler.IndexEstimateOf(result, estimate))
}

// adminIndexStatus reports what the attached index holds.
//
// Cheap on purpose, so a screen can poll it: it asks the index about itself
// rather than measuring the corpus, which is what the estimate above does and
// why that one costs a full traversal. It exists because "the build said
// done" and "search is now answered from an index" are different claims, and
// an operator had no way to tell them apart.
func (e *Engine) adminIndexStatus(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	writeJSON(c, http.StatusOK, handler.IndexStatusOf(e.Search.IndexStateOf()))
}

// indexBlockSize is how many entries share a block. It is the value the
// estimator was calibrated against, so passing anything else would report a
// size for an index this build does not produce.
const indexBlockSize = 1024

func indexSourcesOf(scan []core.ScanSource) []search.Source { return stowcloud.SourcesOf(scan) }
