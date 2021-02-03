/*
 * Copyright 2012-2019 the original author or authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package SpringCore

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"github.com/go-spring/spring-logger"
)

// fnBindingArg 存储函数的参数绑定
type fnBindingArg interface {
	// Get 获取函数参数的绑定值，fileLine 是函数所在文件及其行号，日志使用
	Get(assembly beanAssembly, fileLine string) []reflect.Value
}

// fnStringBindingArg 存储一般的函数参数绑定，对应 Option 模式的函数参数
type fnStringBindingArg struct {
	fnType reflect.Type
	fnTags [][]string // 可能包含可变参数

	withReceiver bool // 函数是否包含接收者，也可以假装第一个参数是接收者
}

// newFnStringBindingArg fnStringBindingArg 的构造函数，所有 tag 必须同时有或者同时没有序号。
func newFnStringBindingArg(fnType reflect.Type, withReceiver bool, tags []string) *fnStringBindingArg {

	numIn := fnType.NumIn()

	// 第一个参数是接收者
	if withReceiver {
		numIn -= 1
	}

	// 是否包含可变参数
	variadic := fnType.IsVariadic()
	fnTags := make([][]string, numIn)

	if len(tags) > 0 {
		indexed := false // 是否包含序号

		if tag := tags[0]; tag != "" {
			if i := strings.Index(tag, ":"); i > 0 {
				if _, err := strconv.Atoi(tag[:i]); err == nil {
					indexed = true
				}
			}
		}

		if indexed { // 有序号
			for _, tag := range tags {

				index := strings.Index(tag, ":")
				if index <= 0 {
					panic(fmt.Errorf("tag:\"%s\" should have index", tag))
				}

				i, err := strconv.Atoi(tag[:index])
				if err != nil {
					panic(fmt.Errorf("tag:\"%s\" should have index", tag))
				}

				if i < 0 || i >= numIn {
					panic(fmt.Errorf("indexed tag \"%s\" overflow", tag))
				}

				fnTags[i] = append(fnTags[i], tag[index+1:])

				if len(fnTags[i]) > 1 && (!variadic || i < numIn-1) {
					panic(fmt.Errorf("index %d has %d tags", i, len(fnTags[i])))
				}
			}

		} else { // 无序号
			for i, tag := range tags {

				if index := strings.Index(tag, ":"); index > 0 {
					if _, err := strconv.Atoi(tag[:index]); err == nil {
						panic(fmt.Errorf("tag \"%s\" shouldn't have index", tag))
					}
				}

				if variadic && i >= numIn-1 { // 处理可变参数
					fnTags[numIn-1] = append(fnTags[numIn-1], tag)
				} else {
					fnTags[i] = []string{tag}
					if i >= numIn {
						panic(fmt.Errorf("tag %d:\"%s\" overflow", i, tag))
					}
				}
			}
		}
	}

	return &fnStringBindingArg{fnType, fnTags, withReceiver}
}

// Get 获取函数参数的绑定值，fileLine 是函数所在文件及其行号，日志使用
func (arg *fnStringBindingArg) Get(assembly beanAssembly, fileLine string) []reflect.Value {

	fnType := arg.fnType
	numIn := fnType.NumIn()

	// 第一个参数是接收者
	if arg.withReceiver {
		numIn -= 1
	}

	variadic := fnType.IsVariadic()
	result := make([]reflect.Value, 0)

	for i, tags := range arg.fnTags {

		var it reflect.Type
		if arg.withReceiver {
			it = fnType.In(i + 1)
		} else {
			it = fnType.In(i)
		}

		if variadic && i == numIn-1 { // 可变参数
			et := it.Elem() // 数组类型
			for _, tag := range tags {
				ev := reflect.New(et).Elem()
				arg.getArgValue(ev, tag, assembly, fileLine)
				result = append(result, ev)
			}
		} else {
			var tag string
			if len(tags) > 0 {
				tag = tags[0]
			}
			iv := reflect.New(it).Elem()
			arg.getArgValue(iv, tag, assembly, fileLine)
			result = append(result, iv)
		}
	}

	return result
}

// getArgValue 获取绑定参数值
func (arg *fnStringBindingArg) getArgValue(v reflect.Value, tag string, assembly beanAssembly, fileLine string) {

	description := fmt.Sprintf("tag:\"%s\" %s", tag, fileLine)
	SpringLogger.Tracef("get value %s", description)

	if ctx := assembly.applicationContext(); IsValueType(v.Kind()) { // 值类型，采用属性绑定语法
		if tag == "" {
			tag = "${}"
		}
		bindStructField(ctx, v, tag, bindOption{})
	} else { // 引用类型，采用对象注入语法
		assembly.wireStructField(v, tag, reflect.Value{}, "")
	}

	SpringLogger.Tracef("get value success %s", description)
}

// fnOptionBindingArg 存储 Option 模式函数的参数绑定
type fnOptionBindingArg struct {
	options []*optionArg
}

// Get 获取函数参数的绑定值，fileLine 是函数所在文件及其行号，日志使用
func (arg *fnOptionBindingArg) Get(assembly beanAssembly, fileLine string) []reflect.Value {
	result := make([]reflect.Value, 0)
	for _, option := range arg.options {
		if v, ok := option.call(assembly); ok {
			result = append(result, v)
		}
	}
	return result
}

// optionArg Option 函数的绑定参数
type optionArg struct {
	cond Condition // 判断条件

	fn  interface{}
	arg fnBindingArg

	file string // 注册点所在文件
	line int    // 注册点所在行数
}

// 判断是否是合法的 Option 函数，只能有一个返回值
func validOptionFunc(fnType reflect.Type) bool {
	return fnType.Kind() == reflect.Func && fnType.NumOut() == 1
}

// NewOptionArg optionArg 的构造函数，tags 是 Option 函数的一般参数绑定
func NewOptionArg(fn interface{}, tags ...string) *optionArg {

	var (
		file string
		line int
	)

	// 获取注册点信息
	for i := 1; i < 10; i++ {
		_, file0, line0, _ := runtime.Caller(i)

		// 排除 spring-core 包下面所有的非 test 文件
		if strings.Contains(file0, "/spring-core/") {
			if !strings.HasSuffix(file0, "_test.go") {
				continue
			}
		}

		file = file0
		line = line0
		break
	}

	fnType := reflect.TypeOf(fn)
	if ok := validOptionFunc(fnType); !ok {
		panic(errors.New("option func must be func(...)option"))
	}

	return &optionArg{
		fn:   fn,
		arg:  newFnStringBindingArg(fnType, false, tags),
		file: file,
		line: line,
	}
}

func (arg *optionArg) FileLine() string {
	return fmt.Sprintf("%s:%d", arg.file, arg.line)
}

// WithCondition 为 optionArg 设置一个 Condition
func (arg *optionArg) WithCondition(cond Condition) *optionArg {
	arg.cond = cond
	return arg
}

// call 获取 optionArg 的运算值
func (arg *optionArg) call(assembly beanAssembly) (v reflect.Value, ok bool) {
	SpringLogger.Tracef("call option func %s", arg.FileLine())

	if arg.cond == nil || arg.cond.Matches(assembly.applicationContext()) {
		fnValue := reflect.ValueOf(arg.fn)
		in := arg.arg.Get(assembly, arg.FileLine())
		out := fnValue.Call(in)
		v = out[0]
		ok = true
	}

	SpringLogger.Tracef("call option func success %s", arg.FileLine())
	return
}
