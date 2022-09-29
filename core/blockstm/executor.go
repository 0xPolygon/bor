package blockstm

import (
	"container/heap"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

type ExecResult struct {
	err      error
	ver      Version
	txIn     TxnInput
	txOut    TxnOutput
	txAllOut TxnOutput
}

type ExecTask interface {
	Execute(mvh *MVHashMap, incarnation int) error
	MVReadList() []ReadDescriptor
	MVWriteList() []WriteDescriptor
	MVFullWriteList() []WriteDescriptor
	Sender() common.Address
	Settle()
	Dependencies() []int
}

type ExecVersionView struct {
	ver    Version
	et     ExecTask
	mvh    *MVHashMap
	sender common.Address
}

func (ev *ExecVersionView) Execute() (er ExecResult) {
	er.ver = ev.ver
	if er.err = ev.et.Execute(ev.mvh, ev.ver.Incarnation); er.err != nil {
		return
	}

	er.txIn = ev.et.MVReadList()
	er.txOut = ev.et.MVWriteList()
	er.txAllOut = ev.et.MVFullWriteList()

	return
}

type ErrExecAbortError struct {
	Dependency int
}

func (e ErrExecAbortError) Error() string {
	if e.Dependency >= 0 {
		return fmt.Sprintf("Execution aborted due to dependency %d", e.Dependency)
	} else {
		return "Execution aborted"
	}
}

type IntHeap []int

func (h IntHeap) Len() int           { return len(h) }
func (h IntHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h IntHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *IntHeap) Push(x any) {
	// Push and Pop use pointer receivers because they modify the slice's length,
	// not just its contents.
	*h = append(*h, x.(int))
}

func (h *IntHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]

	return x
}

// A thread safe priority queue
type SafePriorityQueue struct {
	m     sync.Mutex
	queue *IntHeap
	data  map[int]interface{}
}

func NewSafePriorityQueue(capacity int) *SafePriorityQueue {
	q := make(IntHeap, 0, capacity)

	return &SafePriorityQueue{
		m:     sync.Mutex{},
		queue: &q,
		data:  make(map[int]interface{}, capacity),
	}
}

func (pq *SafePriorityQueue) Push(v int, d interface{}) {
	pq.m.Lock()

	heap.Push(pq.queue, v)
	pq.data[v] = d

	pq.m.Unlock()
}

func (pq *SafePriorityQueue) Pop() interface{} {
	pq.m.Lock()
	defer pq.m.Unlock()

	v := heap.Pop(pq.queue).(int)

	return pq.data[v]
}

func (pq *SafePriorityQueue) Len() int {
	return pq.queue.Len()
}

type ParallelExecutionResult struct {
	TxIO    *TxnInputOutput
	Stats   *[][]uint64
	AllDeps map[int][]int
}

const numGoProcs = 2
const numSpeculativeProcs = 8

type ParallelExecutor struct {
	tasks []ExecTask

	// Stores the execution statistics for each task
	stats      [][]uint64
	statsMutex sync.Mutex

	// Channel for tasks that should be prioritized
	chTasks chan ExecVersionView

	// Channel for speculative tasks
	chSpeculativeTasks chan struct{}

	// Channel to signal that the result of a transaction could be written to storage
	specTaskQueue *SafePriorityQueue

	// A priority queue that stores speculative tasks
	chSettle chan int

	// Channel to signal that a transaction has finished executing
	chResults chan struct{}

	// A priority queue that stores the transaction index of results, so we can validate the results in order
	resultQueue *SafePriorityQueue

	// A wait group to wait for all settling tasks to finish
	settleWg sync.WaitGroup

	// An integer that tracks the index of last settled transaction
	lastSettled int

	// For a task that runs only after all of its preceding tasks have finished and passed validation,
	// its result will be absolutely valid and therefore its validation could be skipped.
	// This map stores the boolean value indicating whether a task satisfy this condition ( absolutely valid).
	skipCheck map[int]bool

	// Execution tasks stores the state of each execution task
	execTasks taskStatusManager

	// Validate tasks stores the state of each validation task
	validateTasks taskStatusManager

	// Stats for debugging purposes
	cntExec, cntSuccess, cntAbort, cntTotalValidations, cntValidationFail int

	diagExecSuccess, diagExecAbort []int

	// Multi-version hash map
	mvh *MVHashMap

	// Stores the inputs and outputs of the last incardanotion of all transactions
	lastTxIO *TxnInputOutput

	// Tracks the incarnation number of each transaction
	txIncarnations []int

	// A map that stores the estimated dependency of a transaction if it is aborted without any known dependency
	estimateDeps map[int][]int

	// A map that records whether a transaction result has been speculatively validated
	preValidated map[int]bool

	// Time records when the parallel execution starts
	begin time.Time

	// Enable profiling
	profile bool

	// Enable metadata
	metadata bool

	// Worker wait group
	workerWg sync.WaitGroup
}

func NewParallelExecutor(tasks []ExecTask, profile bool, metadata bool) *ParallelExecutor {
	numTasks := len(tasks)

	pe := &ParallelExecutor{
		tasks:              tasks,
		stats:              make([][]uint64, numTasks),
		chTasks:            make(chan ExecVersionView, numTasks),
		chSpeculativeTasks: make(chan struct{}, numTasks),
		chSettle:           make(chan int, numTasks),
		chResults:          make(chan struct{}, numTasks),
		specTaskQueue:      NewSafePriorityQueue(numTasks),
		resultQueue:        NewSafePriorityQueue(numTasks),
		lastSettled:        -1,
		skipCheck:          make(map[int]bool),
		execTasks:          makeStatusManager(numTasks),
		validateTasks:      makeStatusManager(0),
		diagExecSuccess:    make([]int, numTasks),
		diagExecAbort:      make([]int, numTasks),
		mvh:                MakeMVHashMap(),
		lastTxIO:           MakeTxnInputOutput(numTasks),
		txIncarnations:     make([]int, numTasks),
		estimateDeps:       make(map[int][]int),
		preValidated:       make(map[int]bool),
		begin:              time.Now(),
		profile:            profile,
		metadata:           metadata,
	}

	return pe
}

func (pe *ParallelExecutor) Prepare() {
	for i, t := range pe.tasks {
		pe.skipCheck[i] = false
		pe.estimateDeps[i] = make([]int, 0)

		if pe.metadata {
			for _, tx := range t.Dependencies() {
				pe.execTasks.addDependencies(tx, i)
				pe.execTasks.clearPending(i)
			}
		} else {
			prevSenderTx := make(map[common.Address]int)

			if tx, ok := prevSenderTx[t.Sender()]; ok {
				pe.execTasks.addDependencies(tx, i)
				pe.execTasks.clearPending(i)
			}

			prevSenderTx[t.Sender()] = i
		}
	}

	pe.workerWg.Add(numSpeculativeProcs + numGoProcs)

	// Launch workers that execute transactions
	for i := 0; i < numSpeculativeProcs+numGoProcs; i++ {
		go func(procNum int) {
			defer pe.workerWg.Done()

			doWork := func(task ExecVersionView) {
				start := time.Duration(0)
				if pe.profile {
					start = time.Since(pe.begin)
				}

				res := task.Execute()

				if res.err == nil {
					pe.mvh.FlushMVWriteSet(res.txAllOut)
				}

				pe.resultQueue.Push(res.ver.TxnIndex, res)
				pe.chResults <- struct{}{}

				if pe.profile {
					end := time.Since(pe.begin)

					stat := []uint64{uint64(res.ver.TxnIndex), uint64(res.ver.Incarnation), uint64(start), uint64(end), uint64(procNum)}

					pe.statsMutex.Lock()
					pe.stats = append(pe.stats, stat)
					pe.statsMutex.Unlock()
				}
			}

			if procNum < numSpeculativeProcs {
				for range pe.chSpeculativeTasks {
					doWork(pe.specTaskQueue.Pop().(ExecVersionView))
				}
			} else {
				for task := range pe.chTasks {
					doWork(task)
				}
			}
		}(i)
	}

	pe.settleWg.Add(len(pe.tasks))

	go func() {
		for t := range pe.chSettle {
			pe.tasks[t].Settle()
			pe.settleWg.Done()
		}
	}()

	// bootstrap first execution
	tx := pe.execTasks.takeNextPending()
	if tx != -1 {
		pe.cntExec++

		pe.chTasks <- ExecVersionView{ver: Version{tx, 0}, et: pe.tasks[tx], mvh: pe.mvh, sender: pe.tasks[tx].Sender()}
	}
}

// nolint: gocognit
func (pe *ParallelExecutor) Step(res ExecResult) (result ParallelExecutionResult, err error) {
	tx := res.ver.TxnIndex

	if _, ok := res.err.(ErrExecAbortError); res.err != nil && !ok {
		err = res.err
		return
	}

	// nolint: nestif
	if execErr, ok := res.err.(ErrExecAbortError); ok {
		addedDependencies := false

		if execErr.Dependency >= 0 {
			l := len(pe.estimateDeps[tx])
			for l > 0 && pe.estimateDeps[tx][l-1] > execErr.Dependency {
				pe.execTasks.removeDependency(pe.estimateDeps[tx][l-1])
				pe.estimateDeps[tx] = pe.estimateDeps[tx][:l-1]
				l--
			}

			addedDependencies = pe.execTasks.addDependencies(execErr.Dependency, tx)
		} else {
			estimate := 0

			if len(pe.estimateDeps[tx]) > 0 {
				estimate = pe.estimateDeps[tx][len(pe.estimateDeps[tx])-1]
			}
			addedDependencies = pe.execTasks.addDependencies(estimate, tx)
			newEstimate := estimate + (estimate+tx)/2
			if newEstimate >= tx {
				newEstimate = tx - 1
			}
			pe.estimateDeps[tx] = append(pe.estimateDeps[tx], newEstimate)
		}

		pe.execTasks.clearInProgress(tx)

		if !addedDependencies {
			pe.execTasks.pushPending(tx)
		}
		pe.txIncarnations[tx]++
		pe.diagExecAbort[tx]++
		pe.cntAbort++
	} else {
		pe.lastTxIO.recordRead(tx, res.txIn)

		if res.ver.Incarnation == 0 {
			pe.lastTxIO.recordWrite(tx, res.txOut)
			pe.lastTxIO.recordAllWrite(tx, res.txAllOut)
		} else {
			if res.txAllOut.hasNewWrite(pe.lastTxIO.AllWriteSet(tx)) {
				pe.validateTasks.pushPendingSet(pe.execTasks.getRevalidationRange(tx + 1))
			}

			prevWrite := pe.lastTxIO.AllWriteSet(tx)

			// Remove entries that were previously written but are no longer written

			cmpMap := make(map[Key]bool)

			for _, w := range res.txAllOut {
				cmpMap[w.Path] = true
			}

			for _, v := range prevWrite {
				if _, ok := cmpMap[v.Path]; !ok {
					pe.mvh.Delete(v.Path, tx)
				}
			}

			pe.lastTxIO.recordWrite(tx, res.txOut)
			pe.lastTxIO.recordAllWrite(tx, res.txAllOut)
		}

		pe.validateTasks.pushPending(tx)
		pe.execTasks.markComplete(tx)
		pe.diagExecSuccess[tx]++
		pe.cntSuccess++

		pe.execTasks.removeDependency(tx)
	}

	// do validations ...
	maxComplete := pe.execTasks.maxAllComplete()

	toValidate := make([]int, 0, 2)

	for pe.validateTasks.minPending() <= maxComplete && pe.validateTasks.minPending() >= 0 {
		toValidate = append(toValidate, pe.validateTasks.takeNextPending())
	}

	for i := 0; i < len(toValidate); i++ {
		pe.cntTotalValidations++

		tx := toValidate[i]

		if pe.skipCheck[tx] || ValidateVersion(tx, pe.lastTxIO, pe.mvh) {
			pe.validateTasks.markComplete(tx)
		} else {
			pe.cntValidationFail++
			pe.diagExecAbort[tx]++
			for _, v := range pe.lastTxIO.AllWriteSet(tx) {
				pe.mvh.MarkEstimate(v.Path, tx)
			}
			// 'create validation tasks for all transactions > tx ...'
			pe.validateTasks.pushPendingSet(pe.execTasks.getRevalidationRange(tx + 1))
			pe.validateTasks.clearInProgress(tx) // clear in progress - pending will be added again once new incarnation executes

			pe.execTasks.clearComplete(tx)
			pe.execTasks.pushPending(tx)

			pe.preValidated[tx] = false
			pe.txIncarnations[tx]++
		}
	}

	// Settle transactions that have been validated to be correct and that won't be re-executed again
	maxValidated := pe.validateTasks.maxAllComplete()

	for pe.lastSettled < maxValidated {
		pe.lastSettled++
		if pe.execTasks.checkInProgress(pe.lastSettled) || pe.execTasks.checkPending(pe.lastSettled) || pe.execTasks.isBlocked(pe.lastSettled) {
			pe.lastSettled--
			break
		}
		pe.chSettle <- pe.lastSettled
	}

	if pe.validateTasks.countComplete() == len(pe.tasks) && pe.execTasks.countComplete() == len(pe.tasks) {
		log.Info("**** blockstm exec summary", "execs", pe.cntExec, "success", pe.cntSuccess, "aborts", pe.cntAbort, "validations", pe.cntTotalValidations, "failures", pe.cntValidationFail, "#tasks/#execs", fmt.Sprintf("%.2f%%", float64(len(pe.tasks))/float64(pe.cntExec)*100))

		close(pe.chTasks)
		close(pe.chSpeculativeTasks)
		pe.workerWg.Wait()
		close(pe.chResults)
		pe.settleWg.Wait()
		close(pe.chSettle)

		var allDeps map[int][]int

		if pe.profile {
			allDeps = GetDep(*pe.lastTxIO)
		}

		return ParallelExecutionResult{pe.lastTxIO, &pe.stats, allDeps}, err
	}

	// Send the next immediate pending transaction to be executed
	if pe.execTasks.minPending() != -1 && pe.execTasks.minPending() == maxValidated+1 {
		nextTx := pe.execTasks.takeNextPending()
		if nextTx != -1 {
			pe.cntExec++

			pe.skipCheck[nextTx] = true

			pe.chTasks <- ExecVersionView{ver: Version{nextTx, pe.txIncarnations[nextTx]}, et: pe.tasks[nextTx], mvh: pe.mvh, sender: pe.tasks[nextTx].Sender()}
		}
	}

	// Send speculative tasks
	for pe.execTasks.minPending() != -1 || len(pe.execTasks.inProgress) == 0 {
		nextTx := pe.execTasks.takeNextPending()

		if nextTx == -1 {
			nextTx = pe.execTasks.takeNextPending()
		}

		if nextTx != -1 {
			pe.cntExec++

			task := ExecVersionView{ver: Version{nextTx, pe.txIncarnations[nextTx]}, et: pe.tasks[nextTx], mvh: pe.mvh, sender: pe.tasks[nextTx].Sender()}

			pe.specTaskQueue.Push(nextTx, task)
			pe.chSpeculativeTasks <- struct{}{}
		}
	}

	return
}

type PropertyCheck func(*ParallelExecutor) error

func executeParallelWithCheck(tasks []ExecTask, profile bool, metadata bool, check PropertyCheck) (result ParallelExecutionResult, err error) {
	if len(tasks) == 0 {
		return ParallelExecutionResult{MakeTxnInputOutput(len(tasks)), nil, nil}, nil
	}

	pe := NewParallelExecutor(tasks, profile, metadata)
	pe.Prepare()

	for range pe.chResults {
		res := pe.resultQueue.Pop().(ExecResult)

		result, err = pe.Step(res)

		if err != nil {
			return result, err
		}

		if check != nil {
			err = check(pe)
		}

		if result.TxIO != nil || err != nil {
			return result, err
		}
	}

	return
}

func ExecuteParallel(tasks []ExecTask, profile bool, metadata bool) (result ParallelExecutionResult, err error) {
	return executeParallelWithCheck(tasks, profile, metadata, func(pe *ParallelExecutor) error {
		return nil
	})
}
