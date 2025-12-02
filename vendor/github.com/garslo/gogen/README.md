# gogen

A simplification of Go's `go/ast` package that allows for some
interesting code generation. Currently very rough.

# Examples

## Hello World
```go
package main

import (
	"os"
	. "github.com/garslo/gogen"
)

func main() {
	pkg := Package{Name: "main"}
	pkg.Declare(Import{"fmt"})
	pkg.Declare(Function{
		Name: "main",
		Body: []Statement{
			CallFunction{
				Func:   Dotted{Var{"fmt"}, "Println"},
				Params: []Expression{Var{`"Hello World!"`}},
			},
		},
	})
	pkg.WriteTo(os.Stdout)
}
```

Output:

```go
package main

import "fmt"

func main() {
	fmt.Println("Hello World!")
}
```

## More
See the
[examples](https://github.com/garslo/gogen/tree/master/examples)
directory for more examples and a build/run script.

```sh
$ ./run-example.sh for_loop.go
CODE:
package main

import "os"
import "fmt"

func main() {
	var i int
	for i = 0; i <= 10; i++ {
		fmt.Println(i)
	}
	os.Exit(i)
}
RUN RESULT:
0
1
2
3
4
5
6
7
8
9
10
exit status 11
```
