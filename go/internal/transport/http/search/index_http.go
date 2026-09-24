//go:build linux

package search

import (
	"net/http"

	"github.com/gin-gonic/gin"
	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/search/stowcloud"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	searchlib "github.com/stowcloud/namesearch"
)

const indexBlockSize = 1024

func (m *Manager) adminIndexEstimate(c *gin.Context) {
	if _, ok := m.admin(c); !ok {
		return
	}
	result, err := searchlib.ScanCorpus(c.Request.Context(), indexSourcesOf(m.Core.ScanSources()), searchlib.ScanOptions{})
	if err != nil {
		m.failKnown(c, err)
		return
	}
	rate, err := m.State.IndexBuildRate(c.Request.Context())
	if err != nil {
		rate = 0
	}
	m.writeJSON(c, http.StatusOK, handler.IndexEstimateOf(result, searchlib.EstimateNameIndex(result.Stats, indexBlockSize, rate)))
}

func (m *Manager) adminIndexStatus(c *gin.Context) {
	if _, ok := m.admin(c); !ok {
		return
	}
	m.writeJSON(c, http.StatusOK, handler.IndexStatusOf(m.Search.IndexStateOf()))
}

func indexSourcesOf(scan []core.ScanSource) []searchlib.Source { return stowcloud.SourcesOf(scan) }
