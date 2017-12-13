package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/raintank/metrictank/api/models"
	"github.com/raintank/metrictank/api/response"
	"github.com/raintank/metrictank/cluster"
	"github.com/raintank/metrictank/conf"
	"github.com/raintank/metrictank/idx/memory"
	"github.com/raintank/metrictank/mdata"
	"github.com/raintank/metrictank/mdata/cache"
	"gopkg.in/raintank/schema.v1"
)

func newSrv(delSeries, delArchives int, key string) (*Server, *cache.MockCache) {
	srv, _ := NewServer()
	srv.RegisterRoutes()

	mdata.SetSingleAgg(conf.Avg, conf.Min, conf.Max)
	mdata.SetSingleSchema(conf.NewRetentionMT(10, 100, 600, 10, true))

	store := mdata.NewDevnullStore()
	srv.BindBackendStore(store)

	mockCache := cache.NewMockCache()
	mockCache.DelMetricRes = cache.CCDelMetricResult{Series: delSeries, Archives: delArchives}
	metrics := mdata.NewAggMetrics(store, mockCache, false, 0, 0, 0)
	srv.BindMemoryStore(metrics)
	srv.BindCache(mockCache)

	metricIndex := memory.New()
	metricIndex.AddOrUpdate(
		&schema.MetricData{
			Id:       key,
			OrgId:    1,
			Name:     "test.key",
			Metric:   "test.key",
			Interval: 10,
			Value:    1,
		},
		0,
	)
	srv.BindMetricIndex(metricIndex)
	return srv, mockCache
}

func TestMetricDelete(t *testing.T) {
	cluster.Init("default", "test", time.Now(), "http", 6060)

	delSeries := 3
	delArchives := 10
	testKey := "12345"

	srv, cache := newSrv(delSeries, delArchives, testKey)
	req, _ := json.Marshal(models.CCacheDelete{
		Patterns:  []string{"test.*"},
		OrgId:     1,
		Propagate: false,
	})

	ts := httptest.NewServer(srv.Macaron)
	defer ts.Close()

	res, err := http.Post(ts.URL+"/ccache/delete", "application/json", bytes.NewReader(req))
	if err != nil {
		t.Fatalf("There was an error in the request: %s", err)
	}

	respParsed := models.CCacheDeleteResp{}
	buf := new(bytes.Buffer)
	buf.ReadFrom(res.Body)
	json.Unmarshal(buf.Bytes(), &respParsed)

	if len(cache.DelMetricKeys) != 1 || cache.DelMetricKeys[0] != testKey {
		t.Fatalf("Expected that key %s has been deleted, but it has not", testKey)
	}

	if respParsed.DeletedSeries != delSeries || respParsed.DeletedArchives != delArchives {
		t.Fatalf("Expected %d series and %d archives to get deleted, but got %d and %d", delSeries, delArchives, respParsed.DeletedSeries, respParsed.DeletedArchives)
	}
}

func TestMetricDeleteWithErrorInPropagation(t *testing.T) {
	manager := cluster.InitMock()

	expectedDeletedSeries, expectedDeletedArchives := 0, 0

	// define how many series/archives are getting deleted by peer 0
	resp := models.CCacheDeleteResp{
		Peers:           map[string]models.CCacheDeleteResp{"2": {Errors: 1}},
		DeletedSeries:   1,
		DeletedArchives: 1,
		Errors:          1,
	}

	respEncoded := response.NewJson(500, resp, "")
	buf, _ := respEncoded.Body()
	manager.Peers = append(manager.Peers, cluster.NewMockNode(false, "0", buf))

	// define how many series/archives are going to get deleted by this server
	delSeries := 3
	delArchives := 10
	testKey := "12345"

	// add up how many series/archives are expected to be deleted
	expectedDeletedSeries += delSeries
	expectedDeletedArchives += delArchives

	srv, _ := newSrv(delSeries, delArchives, testKey)
	req, err := json.Marshal(models.CCacheDelete{
		Patterns:  []string{"test.*"},
		OrgId:     1,
		Propagate: true,
	})

	ts := httptest.NewServer(srv.Macaron)
	defer ts.Close()

	res, err := http.Post(ts.URL+"/ccache/delete", "application/json", bytes.NewReader(req))
	if err != nil {
		t.Fatalf("There was an error in the request: %s", err)
	}

	expectedCode := 500
	if res.StatusCode != expectedCode {
		buf2 := new(bytes.Buffer)
		buf2.ReadFrom(res.Body)
		respParsed := models.CCacheDeleteResp{}
		json.Unmarshal(buf2.Bytes(), &respParsed)
		t.Fatalf("Expected status code %d, but got %d:\n%+v", expectedCode, res.StatusCode, respParsed)
	}
}

func TestMetricDeletePropagation(t *testing.T) {
	manager := cluster.InitMock()

	expectedDeletedSeries, expectedDeletedArchives := 0, 0

	// define how many series/archives are getting deleted by peer 0
	resp := models.CCacheDeleteResp{
		DeletedSeries:   2,
		DeletedArchives: 5,
	}
	expectedDeletedSeries += resp.DeletedSeries
	expectedDeletedArchives += resp.DeletedArchives
	respEncoded := response.NewJson(200, resp, "")
	buf, _ := respEncoded.Body()
	manager.Peers = append(manager.Peers, cluster.NewMockNode(false, "1", buf))

	// define how many series/archives are getting deleted by peer 1
	resp = models.CCacheDeleteResp{
		Peers:           map[string]models.CCacheDeleteResp{"2": {Errors: 1}},
		DeletedSeries:   1,
		DeletedArchives: 1,
	}

	respEncoded = response.NewJson(200, resp, "")
	buf, _ = respEncoded.Body()
	// should be ignored because peer.IsLocal() is true
	manager.Peers = append(manager.Peers, cluster.NewMockNode(true, "2", buf))

	// define how many series/archives are getting deleted by peer 2
	resp = models.CCacheDeleteResp{
		Peers:           map[string]models.CCacheDeleteResp{"3": {Errors: 1}},
		DeletedSeries:   1,
		DeletedArchives: 3,
	}
	expectedDeletedSeries += resp.DeletedSeries
	expectedDeletedArchives += resp.DeletedArchives
	respEncoded = response.NewJson(200, resp, "")
	buf, _ = respEncoded.Body()
	manager.Peers = append(manager.Peers, cluster.NewMockNode(false, "3", buf))

	// define how many series/archives are going to get deleted by this server
	delSeries := 3
	delArchives := 10
	testKey := "12345"

	// add up how many series/archives are expected to be deleted
	expectedDeletedSeries += delSeries
	expectedDeletedArchives += delArchives

	srv, cache := newSrv(delSeries, delArchives, testKey)
	req, err := json.Marshal(models.CCacheDelete{
		Patterns:  []string{"test.*"},
		OrgId:     1,
		Propagate: true,
	})

	ts := httptest.NewServer(srv.Macaron)
	defer ts.Close()

	res, err := http.Post(ts.URL+"/ccache/delete", "application/json", bytes.NewReader(req))
	if err != nil {
		t.Fatalf("There was an error in the request: %s", err)
	}

	buf2 := new(bytes.Buffer)
	buf2.ReadFrom(res.Body)
	respParsed := models.CCacheDeleteResp{}
	json.Unmarshal(buf2.Bytes(), &respParsed)

	if len(cache.DelMetricKeys) != 1 || cache.DelMetricKeys[0] != testKey {
		t.Fatalf("Expected that key %s has been deleted, but it has not", testKey)
	}

	deletedArchives := respParsed.DeletedArchives
	deletedSeries := respParsed.DeletedSeries
	for _, peer := range respParsed.Peers {
		deletedArchives += peer.DeletedArchives
		deletedSeries += peer.DeletedSeries
	}

	if deletedSeries != expectedDeletedSeries || deletedArchives != expectedDeletedArchives {
		t.Fatalf(
			"Expected %d series and %d archives to get deleted, but got %d and %d",
			expectedDeletedSeries, expectedDeletedArchives, respParsed.DeletedSeries, respParsed.DeletedArchives,
		)
	}
}
