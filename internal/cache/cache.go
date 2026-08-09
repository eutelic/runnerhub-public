package cache
import("crypto/sha256";"encoding/hex";"errors";"io";"os";"path/filepath";"strings")
var ErrTooLarge=errors.New("cache object exceeds maximum size")
type Store struct{Root string;MaxBytes int64}
func(s Store)Put(r io.Reader)(string,int64,error){if s.Root==""{return "",0,errors.New("cache root required")};max:=s.MaxBytes;if max<=0{max=8<<30};if err:=os.MkdirAll(s.Root,0700);err!=nil{return "",0,err};tmp,err:=os.CreateTemp(s.Root,"upload-");if err!=nil{return "",0,err};defer os.Remove(tmp.Name());h:=sha256.New();n,err:=io.Copy(io.MultiWriter(tmp,h),io.LimitReader(r,max+1));if err!=nil{_=tmp.Close();return "",n,err};if n>max{_=tmp.Close();return "",n,ErrTooLarge};if err:=tmp.Close();err!=nil{return "",n,err};key:=hex.EncodeToString(h.Sum(nil));dst:=s.path(key);if _,err:=os.Stat(dst);err==nil{return key,n,nil};if err:=os.Rename(tmp.Name(),dst);err!=nil{return "",n,err};return key,n,nil}
func(s Store)Open(key string)(*os.File,error){if !valid(key){return nil,errors.New("invalid cache key")};return os.Open(s.path(key))}
func(s Store)Remove(key string)error{if !valid(key){return errors.New("invalid cache key")};err:=os.Remove(s.path(key));if os.IsNotExist(err){return nil};return err}
func(s Store)path(key string)string{return filepath.Join(filepath.Clean(s.Root),strings.ToLower(key))}
func valid(k string)bool{if len(k)!=64{return false};for _,r:=range k{if !(r>='0'&&r<='9'||r>='a'&&r<='f'||r>='A'&&r<='F'){return false}};return true}
