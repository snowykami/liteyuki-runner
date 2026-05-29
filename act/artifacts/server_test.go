// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2021 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package artifacts

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type writableMapFile struct {
	fstest.MapFile
}

func (f *writableMapFile) Write(data []byte) (int, error) {
	f.Data = data
	return len(data), nil
}

func (f *writableMapFile) Close() error {
	return nil
}

type writeMapFS struct {
	fstest.MapFS
}

func (fsys writeMapFS) OpenWritable(name string) (WritableFile, error) {
	file := &writableMapFile{
		MapFile: fstest.MapFile{
			Data: []byte("content2"),
		},
	}
	fsys.MapFS[name] = &file.MapFile

	return file, nil
}

func (fsys writeMapFS) OpenAppendable(name string) (WritableFile, error) {
	file := &writableMapFile{
		MapFile: fstest.MapFile{
			Data: []byte("content2"),
		},
	}
	fsys.MapFS[name] = &file.MapFile

	return file, nil
}

func TestNewArtifactUploadPrepare(t *testing.T) {
	assert := assert.New(t)

	memfs := fstest.MapFS(map[string]*fstest.MapFile{})

	router := httprouter.New()
	uploads(router, "artifact/server/path", writeMapFS{memfs})

	req, _ := http.NewRequest(http.MethodPost, "http://localhost/_apis/pipelines/workflows/1/artifacts", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		assert.Fail("Wrong status")
	}

	response := FileContainerResourceURL{}
	err := json.Unmarshal(rr.Body.Bytes(), &response)
	if err != nil {
		panic(err)
	}

	assert.Equal("http://localhost/upload/1", response.FileContainerResourceURL)
}

func TestArtifactUploadBlob(t *testing.T) {
	assert := assert.New(t)

	memfs := fstest.MapFS(map[string]*fstest.MapFile{})

	router := httprouter.New()
	uploads(router, "artifact/server/path", writeMapFS{memfs})

	req, _ := http.NewRequest(http.MethodPut, "http://localhost/upload/1?itemPath=some/file", strings.NewReader("content"))
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		assert.Fail("Wrong status")
	}

	response := ResponseMessage{}
	err := json.Unmarshal(rr.Body.Bytes(), &response)
	if err != nil {
		panic(err)
	}

	assert.Equal("success", response.Message)
	assert.Equal("content", string(memfs["artifact/server/path/1/some/file"].Data))
}

func TestFinalizeArtifactUpload(t *testing.T) {
	assert := assert.New(t)

	memfs := fstest.MapFS(map[string]*fstest.MapFile{})

	router := httprouter.New()
	uploads(router, "artifact/server/path", writeMapFS{memfs})

	req, _ := http.NewRequest(http.MethodPatch, "http://localhost/_apis/pipelines/workflows/1/artifacts", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		assert.Fail("Wrong status")
	}

	response := ResponseMessage{}
	err := json.Unmarshal(rr.Body.Bytes(), &response)
	if err != nil {
		panic(err)
	}

	assert.Equal("success", response.Message)
}

func TestListArtifacts(t *testing.T) {
	assert := assert.New(t)

	memfs := fstest.MapFS(map[string]*fstest.MapFile{
		"artifact/server/path/1/file.txt": {
			Data: []byte(""),
		},
	})

	router := httprouter.New()
	downloads(router, "artifact/server/path", memfs)

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/_apis/pipelines/workflows/1/artifacts", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		assert.FailNow(fmt.Sprintf("Wrong status: %d", status))
	}

	response := NamedFileContainerResourceURLResponse{}
	err := json.Unmarshal(rr.Body.Bytes(), &response)
	if err != nil {
		panic(err)
	}

	assert.Equal(1, response.Count)
	assert.Equal("file.txt", response.Value[0].Name)
	assert.Equal("http://localhost/download/1", response.Value[0].FileContainerResourceURL)
}

func TestListArtifactContainer(t *testing.T) {
	assert := assert.New(t)

	memfs := fstest.MapFS(map[string]*fstest.MapFile{
		"artifact/server/path/1/some/file": {
			Data: []byte(""),
		},
	})

	router := httprouter.New()
	downloads(router, "artifact/server/path", memfs)

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/download/1?itemPath=some/file", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		assert.FailNow(fmt.Sprintf("Wrong status: %d", status))
	}

	response := ContainerItemResponse{}
	err := json.Unmarshal(rr.Body.Bytes(), &response)
	if err != nil {
		panic(err)
	}

	assert.Len(response.Value, 1)
	assert.Equal("some/file", response.Value[0].Path)
	assert.Equal("file", response.Value[0].ItemType)
	assert.Equal("http://localhost/artifact/1/some/file/.", response.Value[0].ContentLocation)
}

func TestDownloadArtifactFile(t *testing.T) {
	assert := assert.New(t)

	memfs := fstest.MapFS(map[string]*fstest.MapFile{
		"artifact/server/path/1/some/file": {
			Data: []byte("content"),
		},
	})

	router := httprouter.New()
	downloads(router, "artifact/server/path", memfs)

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/artifact/1/some/file", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		assert.FailNow(fmt.Sprintf("Wrong status: %d", status))
	}

	data := rr.Body.Bytes()

	assert.Equal("content", string(data))
}

// TestArtifactFlow drives the real Serve() artifact server over a loopback socket, exercising
// the same upload -> finalize -> list -> download protocol the upload-artifact/download-artifact
// actions speak. Running it in-process (rather than from a job container) keeps it network-free
// and reachable everywhere, including when the CI job is itself a container.
func TestArtifactFlow(t *testing.T) {
	artifactPath := t.TempDir()

	// Serve the exact routes Serve() wires up, on a real loopback socket via httptest. httptest
	// picks a free port and Close() tears the server down synchronously — avoiding both the
	// port-rebind race and Serve()'s detached ListenAndServe goroutine, which logger.Fatal()s
	// (process exit) on a bind error and can outlive the test's temp-dir cleanup.
	router := httprouter.New()
	fsys := readWriteFSImpl{}
	uploads(router, artifactPath, fsys)
	downloads(router, artifactPath, fsys)
	server := httptest.NewServer(router)
	defer server.Close()

	baseURL := server.URL
	client := server.Client()
	client.Timeout = 5 * time.Second

	// request performs one HTTP call and returns the status and body. The default transport adds
	// Accept-Encoding: gzip and transparently decompresses, so gzipped downloads come back plain.
	request := func(t *testing.T, method, rawURL string, body io.Reader, header http.Header) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, rawURL, body)
		require.NoError(t, err)
		maps.Copy(req.Header, header)
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, data
	}

	t.Run("upload-and-download", func(t *testing.T) {
		const runID, item, content = "1", "my-artifact/data.txt", "hello artifact\n"

		status, data := request(t, http.MethodPost, baseURL+"/_apis/pipelines/workflows/"+runID+"/artifacts", nil, nil)
		require.Equal(t, http.StatusOK, status, string(data))
		var prep FileContainerResourceURL
		require.NoError(t, json.Unmarshal(data, &prep))
		require.Equal(t, baseURL+"/upload/"+runID, prep.FileContainerResourceURL)

		status, data = request(t, http.MethodPut, prep.FileContainerResourceURL+"?itemPath="+url.QueryEscape(item), strings.NewReader(content), nil)
		require.Equal(t, http.StatusOK, status, string(data))
		var msg ResponseMessage
		require.NoError(t, json.Unmarshal(data, &msg))
		require.Equal(t, "success", msg.Message)

		status, data = request(t, http.MethodPatch, baseURL+"/_apis/pipelines/workflows/"+runID+"/artifacts", nil, nil)
		require.Equal(t, http.StatusOK, status, string(data))

		status, data = request(t, http.MethodGet, baseURL+"/_apis/pipelines/workflows/"+runID+"/artifacts", nil, nil)
		require.Equal(t, http.StatusOK, status, string(data))
		var list NamedFileContainerResourceURLResponse
		require.NoError(t, json.Unmarshal(data, &list))
		require.Equal(t, 1, list.Count)
		require.Equal(t, "my-artifact", list.Value[0].Name)

		status, data = request(t, http.MethodGet, list.Value[0].FileContainerResourceURL+"?itemPath=my-artifact", nil, nil)
		require.Equal(t, http.StatusOK, status, string(data))
		var items ContainerItemResponse
		require.NoError(t, json.Unmarshal(data, &items))
		require.Len(t, items.Value, 1)
		require.Equal(t, "file", items.Value[0].ItemType)
		require.Equal(t, "my-artifact/data.txt", items.Value[0].Path)

		status, data = request(t, http.MethodGet, items.Value[0].ContentLocation, nil, nil)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, content, string(data))

		stored, err := os.ReadFile(filepath.Join(artifactPath, runID, "my-artifact", "data.txt"))
		require.NoError(t, err)
		require.Equal(t, content, string(stored))
	})

	t.Run("gzip-roundtrip", func(t *testing.T) {
		const runID, item, content = "2", "logs/app.log", "compressed payload\n"

		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, err := gz.Write([]byte(content))
		require.NoError(t, err)
		require.NoError(t, gz.Close())

		status, data := request(t, http.MethodPut, baseURL+"/upload/"+runID+"?itemPath="+url.QueryEscape(item),
			&buf, http.Header{"Content-Encoding": []string{"gzip"}})
		require.Equal(t, http.StatusOK, status, string(data))

		// stored compressed, with the server's gzip marker suffix
		_, err = os.Stat(filepath.Join(artifactPath, runID, "logs", "app.log.gz__"))
		require.NoError(t, err)

		status, data = request(t, http.MethodGet, baseURL+"/download/"+runID+"?itemPath=logs", nil, nil)
		require.Equal(t, http.StatusOK, status, string(data))
		var items ContainerItemResponse
		require.NoError(t, json.Unmarshal(data, &items))
		require.Len(t, items.Value, 1)
		require.Equal(t, "logs/app.log", items.Value[0].Path)

		status, data = request(t, http.MethodGet, items.Value[0].ContentLocation, nil, nil)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, content, string(data))
	})

	// GHSL-2023-004: an itemPath that climbs out of the run directory must be neutralised so the
	// blob cannot be written outside the artifact root.
	t.Run("GHSL-2023-004", func(t *testing.T) {
		const runID, content = "3", "contained\n"

		status, data := request(t, http.MethodPut, baseURL+"/upload/"+runID+"?itemPath="+url.QueryEscape("../../escape.txt"),
			strings.NewReader(content), nil)
		require.Equal(t, http.StatusOK, status, string(data))

		stored, err := os.ReadFile(filepath.Join(artifactPath, runID, "escape.txt"))
		require.NoError(t, err)
		require.Equal(t, content, string(stored))

		_, err = os.Stat(filepath.Join(filepath.Dir(artifactPath), "escape.txt"))
		require.True(t, os.IsNotExist(err), "upload escaped the artifact root")

		status, data = request(t, http.MethodGet, baseURL+"/artifact/"+runID+"/escape.txt", nil, nil)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, content, string(data))
	})
}

func TestMkdirFsImplSafeResolve(t *testing.T) {
	assert := assert.New(t)

	baseDir := "/foo/bar"

	tests := map[string]struct {
		input string
		want  string
	}{
		"simple":         {input: "baz", want: "/foo/bar/baz"},
		"nested":         {input: "baz/blue", want: "/foo/bar/baz/blue"},
		"dots in middle": {input: "baz/../../blue", want: "/foo/bar/blue"},
		"leading dots":   {input: "../../parent", want: "/foo/bar/parent"},
		"root path":      {input: "/root", want: "/foo/bar/root"},
		"root":           {input: "/", want: "/foo/bar"},
		"empty":          {input: "", want: "/foo/bar"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(tc.want, safeResolve(baseDir, tc.input))
		})
	}
}

func TestDownloadArtifactFileUnsafePath(t *testing.T) {
	assert := assert.New(t)

	memfs := fstest.MapFS(map[string]*fstest.MapFile{
		"artifact/server/path/some/file": {
			Data: []byte("content"),
		},
	})

	router := httprouter.New()
	downloads(router, "artifact/server/path", memfs)

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/artifact/2/../../some/file", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		assert.FailNow(fmt.Sprintf("Wrong status: %d", status))
	}

	data := rr.Body.Bytes()

	assert.Equal("content", string(data))
}

func TestArtifactUploadBlobUnsafePath(t *testing.T) {
	assert := assert.New(t)

	memfs := fstest.MapFS(map[string]*fstest.MapFile{})

	router := httprouter.New()
	uploads(router, "artifact/server/path", writeMapFS{memfs})

	req, _ := http.NewRequest(http.MethodPut, "http://localhost/upload/1?itemPath=../../some/file", strings.NewReader("content"))
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		assert.Fail("Wrong status")
	}

	response := ResponseMessage{}
	err := json.Unmarshal(rr.Body.Bytes(), &response)
	if err != nil {
		panic(err)
	}

	assert.Equal("success", response.Message)
	assert.Equal("content", string(memfs["artifact/server/path/1/some/file"].Data))
}
