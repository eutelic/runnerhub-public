package hostinfo

import "testing"

func TestAvailableMemoryParsingFailsClosed(t *testing.T) {
    tests:=[]struct{input string;want int64}{
        {"MemTotal: 33000000 kB\nMemAvailable: 12000000 kB\n",12000000*1024},
        {"MemTotal: 33000000 kB\n",0},
        {"MemAvailable: unknown kB\n",0},
        {"MemAvailable: -1 kB\n",0},
        {"MemAvailable: 0 kB\n",0},
    }
    for _,test:=range tests {
        if got:=availableMemoryFromMeminfo(test.input);got!=test.want {
            t.Errorf("memory for %q=%d want %d",test.input,got,test.want)
        }
    }
}
