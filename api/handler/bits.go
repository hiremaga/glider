package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/concourse/turbine/api/builds"
	"github.com/pivotal-golang/lager"
)

func (handler *Handler) UploadBits(w http.ResponseWriter, r *http.Request) {
	guid := r.FormValue(":guid")

	handler.buildsMutex.RLock()
	build, found := handler.builds[guid]
	handler.buildsMutex.RUnlock()

	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	log := handler.logger.Session("upload", lager.Data{
		"build": build,
	})

	log.Info("triggering")

	buf := new(bytes.Buffer)

	turbineBuild := builds.Build{
		Guid: build.Guid,

		LogsURL: "ws://" + handler.peerAddr + "/builds/" + build.Guid + "/log/input",

		Privileged: true,

		Config: build.Config,

		Inputs: []builds.Input{
			{
				Name: build.Name,
				Type: "raw",
				Source: builds.Source{
					"uri": "http://" + handler.peerAddr + "/builds/" + build.Guid + "/bits",
				},
			},
		},

		Callback: "http://" + handler.peerAddr + "/builds/" + build.Guid + "/result",
	}

	err := json.NewEncoder(buf).Encode(turbineBuild)
	if err != nil {
		panic(err)
	}

	defer r.Body.Close()

	res, err := http.Post(handler.turbineURL+"/builds", "application/json", buf)
	if err != nil {
		log.Error("failed-to-trigger", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	res.Body.Close()

	if res.StatusCode == http.StatusCreated {
		w.WriteHeader(http.StatusCreated)

		handler.bitsMutex.RLock()
		session := handler.bits[guid]
		handler.bitsMutex.RUnlock()

		session.servingBits.Add(1)
		session.bits <- r
		session.servingBits.Wait()
	} else {
		log.Info("bad-status-code", lager.Data{
			"status": res.Status,
		})
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

func (handler *Handler) DownloadBits(w http.ResponseWriter, r *http.Request) {
	guid := r.FormValue(":guid")

	handler.bitsMutex.RLock()
	session, found := handler.bits[guid]
	handler.bitsMutex.RUnlock()

	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	log := handler.logger.Session("upload", lager.Data{
		"guid": guid,
	})

	var bits *http.Request

	select {
	case bits = <-session.bits:
	case <-time.After(time.Second):
		w.WriteHeader(404)
		return
	}

	log.Info("serve")

	defer session.servingBits.Done()

	w.Header().Set("Content-Type", bits.Header.Get("Content-Type"))

	w.WriteHeader(200)

	_, err := io.Copy(w, bits.Body)
	if err != nil {
		log.Error("failed-to-stream", err)
	}
}
