package blockstm

import (
	"fmt"
	"strings"
	"time"

	"github.com/heimdalr/dag"

	"github.com/ethereum/go-ethereum/log"
)

type DAG struct {
	*dag.DAG
}

func HasReadDep(txFrom TxnOutput, txTo TxnInput) bool {
	reads := make(map[Key]bool)

	for _, v := range txTo {
		reads[v.Path] = true
	}

	for _, rd := range txFrom {
		if _, ok := reads[rd.Path]; ok {
			return true
		}
	}

	return false
}

func BuildDAG(deps TxnInputOutput) (d DAG) {
	d = DAG{dag.NewDAG()}
	ids := make(map[int]string)

	for i := len(deps.inputs) - 1; i > 0; i-- {
		txTo := deps.inputs[i]

		var txToId string

		if _, ok := ids[i]; ok {
			txToId = ids[i]
		} else {
			txToId, _ = d.AddVertex(i)
			ids[i] = txToId
		}

		for j := i - 1; j >= 0; j-- {
			txFrom := deps.allOutputs[j]

			if HasReadDep(txFrom, txTo) {
				var txFromId string
				if _, ok := ids[j]; ok {
					txFromId = ids[j]
				} else {
					txFromId, _ = d.AddVertex(j)
					ids[j] = txFromId
				}

				err := d.AddEdge(txFromId, txToId)
				if err != nil {
					log.Warn("Failed to add edge", "from", txFromId, "to", txToId, "err", err)
				}
			}
		}
	}

	return
}

func GetDep(deps TxnInputOutput) map[int][]int {
	dependencies := map[int][]int{}

	for i := len(deps.inputs) - 1; i > 0; i-- {
		txTo := deps.inputs[i]

		for j := i - 1; j >= 0; j-- {
			txFrom := deps.allOutputs[j]

			if HasReadDep(txFrom, txTo) {
				dependencies[i] = append(dependencies[i], j)
			}
		}
	}

	return dependencies
}

// Find the longest execution path in the DAG
func (d DAG) LongestPath(stats map[int]ExecutionStat) ([]int, uint64) {
	prev := make(map[int]int, len(d.GetVertices()))

	for i := 0; i < len(d.GetVertices()); i++ {
		prev[i] = -1
	}

	pathWeights := make(map[int]uint64, len(d.GetVertices()))

	maxPath := 0
	maxPathWeight := uint64(0)

	idxToId := make(map[int]string, len(d.GetVertices()))

	for k, i := range d.GetVertices() {
		idxToId[i.(int)] = k
	}

	for i := 0; i < len(idxToId); i++ {
		parents, _ := d.GetParents(idxToId[i])

		if len(parents) > 0 {
			for _, p := range parents {
				weight := pathWeights[p.(int)] + stats[i].End - stats[i].Start
				if weight > pathWeights[i] {
					pathWeights[i] = weight
					prev[i] = p.(int)
				}
			}
		} else {
			pathWeights[i] = stats[i].End - stats[i].Start
		}

		if pathWeights[i] > maxPathWeight {
			maxPath = i
			maxPathWeight = pathWeights[i]
		}
	}

	path := make([]int, 0)
	for i := maxPath; i != -1; i = prev[i] {
		path = append(path, i)
	}

	// Reverse the path so the transactions are in the ascending order
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	return path, maxPathWeight
}

func (d DAG) Report(stats map[int]ExecutionStat, out func(string)) {
	longestPath, weight := d.LongestPath(stats)

	serialWeight := uint64(0)

	for i := 0; i < len(d.GetVertices()); i++ {
		serialWeight += stats[i].End - stats[i].Start
	}

	makeStrs := func(ints []int) (ret []string) {
		for _, v := range ints {
			ret = append(ret, fmt.Sprint(v))
		}

		return
	}

	out("Longest execution path:")
	out(fmt.Sprintf("(%v) %v", len(longestPath), strings.Join(makeStrs(longestPath), "->")))

	out(fmt.Sprintf("Longest path ideal execution time: %v of %v (serial total), %v%%", time.Duration(weight),
		time.Duration(serialWeight), fmt.Sprintf("%.1f", float64(weight)*100.0/float64(serialWeight))))
}
