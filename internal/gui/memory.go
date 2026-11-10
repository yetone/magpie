package gui

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/memory"
)

// memoryView is the Library's Memory tab: every memory, newest change
// first, the projects they are kept for, and whether the agents ticked
// for instructions are given the digest.
type memoryView struct {
	Memories []*memory.Memory           `json:"memories"`
	Projects map[string]*memory.Project `json:"projects"`
	Digest   bool                       `json:"digest"`
	Agents   []string                   `json:"agents"` // ticked for instructions
}

func readMemory() (*memoryView, error) {
	ms, ps, err := memory.All()
	if err != nil {
		return nil, err
	}
	on, agents, err := library.MemoryInInstructions()
	if err != nil {
		return nil, err
	}
	if ms == nil {
		ms = []*memory.Memory{}
	}
	if ps == nil {
		ps = map[string]*memory.Project{}
	}
	return &memoryView{Memories: ms, Projects: ps, Digest: on, Agents: agents}, nil
}

// memoryChanged publishes the user's change to the agents' instructions
// and answers with the tab as it is now. A publish that fails is the
// change kept and not yet given out, said beside it.
func memoryChanged(rw http.ResponseWriter) {
	_, perr := library.PublishMemory(false)
	v, err := readMemory()
	if err != nil {
		fail(rw, err)
		return
	}
	if perr != nil {
		writeJSON(rw, struct {
			*memoryView
			Problem string `json:"problem"`
		}{v, perr.Error()})
		return
	}
	writeJSON(rw, v)
}

func memoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory", func(rw http.ResponseWriter, r *http.Request) {
		v, err := readMemory()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// a memory added, or one changed by its id: the user's own from then on
	mux.HandleFunc("POST /api/memory", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
			memory.Input
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		var err error
		if in.ID != "" {
			_, err = memory.Edit(in.ID, in.Input, true)
		} else {
			_, _, err = memory.Save(in.Input, memory.FromUser, nil, nil)
		}
		if err != nil {
			fail(rw, err)
			return
		}
		memoryChanged(rw)
	})
	mux.HandleFunc("POST /api/memory/remove", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if len(in.IDs) == 0 {
			fail(rw, errors.New("name the memories to remove"))
			return
		}
		if err := memory.Remove(in.IDs...); err != nil {
			fail(rw, err)
			return
		}
		memoryChanged(rw)
	})
	// some memories given one status at once: kept, archived or brought
	// back from the Memory tab's Select (RealZeroP27 on X)
	mux.HandleFunc("POST /api/memory/status", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			IDs    []string `json:"ids"`
			Status string   `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if len(in.IDs) == 0 {
			fail(rw, errors.New("name the memories to change"))
			return
		}
		if _, err := memory.SetStatus(in.Status, in.IDs...); err != nil {
			fail(rw, err)
			return
		}
		memoryChanged(rw)
	})
	mux.HandleFunc("POST /api/memory/digest", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if _, err := library.SetMemoryInInstructions(in.On); err != nil {
			fail(rw, err)
			return
		}
		memoryChanged(rw)
	})
}
