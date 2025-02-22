package misc

import (
	"bytes"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"reflect"
	"regexp"
)

// Ref: https://stackoverflow.com/questions/19979178/what-is-the-idiomatic-go-equivalent-of-cs-ternary-operator

func Ternary[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}

func TernaryF[T any](cond bool, fTrue func() T, fFalse func() T) T {
	if cond {
		return fTrue()
	}
	return fFalse()
}

func IsZero(value interface{}) bool {
	return reflect.DeepEqual(value, reflect.Zero(reflect.TypeOf(value)).Interface())
}

func CountNonZero(vals ...interface{}) int {
	cnt := 0
	for b := range vals {
		if !IsZero(vals[b]) {
			cnt++
		}
	}
	return cnt
}

var k8sNameRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func ValidateK8sName(name string) bool {
	return k8sNameRegex.MatchString(name)
}

// Replace yaml.Marshall. Just to be able to set indentation

func map2YamlBuffer(data interface{}) *bytes.Buffer {
	b := bytes.Buffer{}
	encoder := yaml.NewEncoder(&b)
	encoder.SetIndent(2)
	err := encoder.Encode(data)
	if err != nil {
		panic(err)
	}
	return &b
}

func Map2YamlByteA(data interface{}) []byte {
	return map2YamlBuffer(data).Bytes()
}

func Map2YamlStr(data interface{}) string {
	return map2YamlBuffer(data).String()
}

func map2JsonBuffer(data interface{}) *bytes.Buffer {
	b := bytes.Buffer{}
	encoder := json.NewEncoder(&b)
	err := encoder.Encode(data)
	if err != nil {
		panic(err)
	}
	return &b
}

func Map2JsonByteA(data interface{}) []byte {
	return map2JsonBuffer(data).Bytes()
}

// ------------------------------------------

func CopyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func(in *os.File) { _ = in.Close() }(in)
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func(out *os.File) { _ = out.Close() }(out)
	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Close()
}
