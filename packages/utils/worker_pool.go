package utils

import (
	"context"
	"fmt"
	"log/slog"
	"rednote/packages/defaults"
	"runtime/debug"
	"sync"
)

// Option 协程池选项函数类型
type Option func(*WorkerPool)

// WithBufferSize 设置 channel 缓冲区大小
// 默认值 = capacity * 2
func WithBufferSize(size int) Option {
	return func(p *WorkerPool) {
		p.bufferSize = size
		p.taskCh = make(chan Task, size)
	}
}

// WithPanicHandler 设置 panic 回调函数
// 默认行为：记录错误日志 + 堆栈
func WithPanicHandler(handler func(task Task, r any, stack []byte)) Option {
	return func(p *WorkerPool) {
		p.panicHandler = handler
	}
}

// WithLogger 设置日志记录器
func WithLogger(logger *slog.Logger) Option {
	return func(p *WorkerPool) {
		p.logger = logger
	}
}

// ============================================================
// WorkerPool 核心实现
// ============================================================

// Task 是无返回值的任务函数类型
type Task func()

// WorkerPool 是一个通用协程池，支持任务提交、优雅关闭和上下文取消
type WorkerPool struct {
	capacity int
	// bufferSize channel 缓冲区大小，0 表示默认值（capacity*2）
	bufferSize int

	taskCh chan Task

	// panicHandler panic 回调，默认打印到 slog.Error
	panicHandler func(task Task, r any, stack []byte)
	// logger 日志记录器，默认使用 slog.Default()
	logger *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup
}

// NewWorkerPool 创建并启动一个协程池
//   - ctx: 父 context，用于控制整个池的生命周期
//   - capacity: worker 数量，< 0 时使用默认值
//   - opts: 可选配置项
func NewWorkerPool(ctx context.Context, capacity int, opts ...Option) *WorkerPool {
	if capacity <= 0 {
		capacity = defaults.WorkerPoolSize
	}

	ctx, cancel := context.WithCancel(ctx)
	p := &WorkerPool{
		capacity:   capacity,
		bufferSize: capacity * 2,
		logger:     slog.Default(),
		taskCh:     make(chan Task, capacity*2),
		ctx:        ctx,
		cancel:     cancel,
	}

	for _, opt := range opts {
		opt(p)
	}

	// 启动 worker
	for i := 0; i < capacity; i++ {
		p.wg.Add(1)
		go p.worker()
	}

	return p
}

// worker 是单个 worker 的主循环
func (p *WorkerPool) worker() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			// context 取消，drain 剩余任务后退出
			p.drain()
			return
		case task, ok := <-p.taskCh:
			if !ok {
				return
			}
			p.safeExecute(task)
		}
	}
}

// drain 消费 channel 中剩余的任务（不执行，防止 goroutine 泄漏）
func (p *WorkerPool) drain() {
	for {
		select {
		case _, ok := <-p.taskCh:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// safeExecute 安全执行任务，捕获 panic 防止单个任务导致 worker 挂掉
func (p *WorkerPool) safeExecute(task Task) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			if p.panicHandler != nil {
				p.panicHandler(task, r, stack)
			} else {
				p.logger.Error("worker panic recovered",
					"recover", fmt.Sprintf("%v", r),
					"stack", string(stack),
				)
			}
		}
	}()
	task()
}

// AddWork 提交任务到池中
// 返回 nil 表示提交成功，返回 context.Canceled 表示池已关闭
func (p *WorkerPool) AddWork(task Task) error {
	if task == nil {
		return nil
	}

	select {
	case <-p.ctx.Done():
		return context.Canceled
	case p.taskCh <- task:
		return nil
	}
}

// Close 关闭协程池，等待所有已提交任务完成后退出
func (p *WorkerPool) Close() {
	p.cancel()
	close(p.taskCh)
	p.wg.Wait()
}

// CloseNow 立即关闭协程池，不等待剩余任务完成
func (p *WorkerPool) CloseNow() {
	p.cancel()
	close(p.taskCh)
	// 不等待，直接返回
}

// Len 返回当前待处理任务数
func (p *WorkerPool) Len() int {
	return len(p.taskCh)
}

// 泛型版本：支持带返回值的任务
// ResultTask 是带返回值的任务，返回 (结果, error)
type ResultTask[T any] func() (T, error)

// ResultFuture 代表一个异步任务的结果
type ResultFuture[T any] struct {
	done chan struct{}
	val  T
	err  error
}

// Wait 阻塞直到任务完成，返回结果
func (f *ResultFuture[T]) Wait() (T, error) {
	<-f.done
	return f.val, f.err
}

// IsDone 检查任务是否已完成（非阻塞）
func (f *ResultFuture[T]) IsDone() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

// Submit 提交一个带返回值的任务，返回 Future
func Submit[T any](pool *WorkerPool, task ResultTask[T]) *ResultFuture[T] {
	future := &ResultFuture[T]{
		done: make(chan struct{}),
	}

	err := pool.AddWork(func() {
		defer close(future.done)
		future.val, future.err = task()
	})

	if err != nil {
		future.err = err
		close(future.done)
	}

	return future
}
