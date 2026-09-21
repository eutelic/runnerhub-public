package images
import("errors";"sort";"strings";"github.com/eutelic/runnerhub/internal/core")
const LegacyDefaultRunnerImage="ghcr.io/actions/actions-runner:latest"
const DefaultRunnerImage="ghcr.io/actions/actions-runner:2.337.0@sha256:e5496277be5d09bc968b3d64911b74e219ac4a3f2edce956a3ecf9271bea1ef4"
const DefaultDinDImage="docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0"
func UpgradeLegacyDefault(image string)(string,bool){if strings.TrimSpace(image)==LegacyDefaultRunnerImage{return DefaultRunnerImage,true};return image,false}
type InventoryEntry struct{Image string `json:"image"`;Pools []string `json:"pools"`;Workers int `json:"workers"`}
func ValidatePinned(image string)error{image=strings.TrimSpace(image);ref,digest,ok:=strings.Cut(image,"@sha256:");if !ok||strings.TrimSpace(ref)==""||strings.ContainsAny(ref," \t\r\n")||len(digest)!=64{return errors.New("image must be pinned by sha256 digest")};for _,r:=range digest{if !((r>='0'&&r<='9')||(r>='a'&&r<='f')){return errors.New("image must be pinned by lowercase sha256 digest")}};return nil}
func Inventory(pools []core.RunnerPool,workers []core.Worker)[]InventoryEntry{m:=map[string]*InventoryEntry{};for _,p:=range pools{if p.Image==""{continue};e:=m[p.Image];if e==nil{e=&InventoryEntry{Image:p.Image};m[p.Image]=e};e.Pools=append(e.Pools,p.Name)};for _,w:=range workers{if w.Image==""||w.State==core.WorkerDeleted{continue};e:=m[w.Image];if e==nil{e=&InventoryEntry{Image:w.Image};m[w.Image]=e};e.Workers++};out:=make([]InventoryEntry,0,len(m));for _,e:=range m{sort.Strings(e.Pools);out=append(out,*e)};sort.Slice(out,func(i,j int)bool{return out[i].Image<out[j].Image});return out}
