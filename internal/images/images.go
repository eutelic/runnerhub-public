package images

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"github.com/example-user/runnerhub/internal/core"
)
var digestPattern = regexp.MustCompile(`^[a-zA-Z0-9._/-]+@sha256:[a-f0-9]{64}$`)
type InventoryEntry struct { Image string `json:"image"`; Pools []string `json:"pools"`; Workers int `json:"workers"` }
func ValidatePinned(image string) error { if !digestPattern.MatchString(strings.TrimSpace(image)){return errors.New("image must be pinned by sha256 digest")};return nil }
func Inventory(pools []core.RunnerPool,workers []core.Worker) []InventoryEntry { m:=map[string]*InventoryEntry{};for _,p:=range pools{if p.Image==""{continue};e:=m[p.Image];if e==nil{e=&InventoryEntry{Image:p.Image};m[p.Image]=e};e.Pools=append(e.Pools,p.Name)};for _,w:=range workers{if w.Image==""||w.State==core.WorkerDeleted{continue};e:=m[w.Image];if e==nil{e=&InventoryEntry{Image:w.Image};m[w.Image]=e};e.Workers++};out:=make([]InventoryEntry,0,len(m));for _,e:=range m{sort.Strings(e.Pools);out=append(out,*e)};sort.Slice(out,func(i,j int)bool{return out[i].Image<out[j].Image});return out }
