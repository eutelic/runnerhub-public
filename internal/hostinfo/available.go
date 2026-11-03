package hostinfo

import (
    "os"
    "strconv"
    "strings"

    "github.com/eutelic/runnerhub/internal/core"
)

// AvailableCapacity is a live admission preflight, distinct from the stable
// host capacity used for scheduling. RAM already used by the OS is not free
// for an uncontainerized Android emulator, even if MemTotal is large.
func AvailableCapacity(path string) core.Resources {
    result:=Capacity(path)
    if data,err:=os.ReadFile("/proc/meminfo");err==nil {
        result.MemoryBytes=availableMemoryFromMeminfo(string(data))
    }
    return result
}

func availableMemoryFromMeminfo(value string) int64 {
    for _,line:=range strings.Split(value,"\n") {
        if !strings.HasPrefix(line,"MemAvailable:"){continue}
        fields:=strings.Fields(line)
        if len(fields)<2 {return 0}
        kib,err:=strconv.ParseInt(fields[1],10,64)
        if err!=nil||kib<0 {return 0}
        return kib*1024
    }
    return 0
}
