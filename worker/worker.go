// Package worker corre as tarefas periódicas de que a cobrança depende: as
// confirmações que chegam por consulta, as reconciliações, a renovação.
//
// Todas elas têm o mesmo formato e as mesmas duas exigências, que é o que
// justifica um sítio comum. A primeira é sobreviver a um pânico: uma tarefa que
// morre em silêncio leva consigo o processo que confirma pagamentos, e ninguém
// dá por isso até o primeiro cliente reclamar. A segunda é correr uma vez ao
// arranque: sem isso, uma instância acabada de arrancar deixa por tratar tudo o
// que a anterior deixou a meio, durante um intervalo inteiro.
package worker

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Task é uma tarefa periódica.
type Task func(ctx context.Context)

// Job descreve uma tarefa a correr periodicamente.
type Job struct {
	// Name identifica a tarefa nos registos.
	Name string
	// Interval é de quanto em quanto tempo corre.
	Interval time.Duration
	// Timeout limita cada execução. Zero usa o intervalo, para que uma execução
	// presa não se sobreponha à seguinte.
	Timeout time.Duration
	// Run é o que fazer.
	Run Task
	// SkipInitial não corre a tarefa ao arranque. Use-o só quando a execução
	// imediata for indesejada (por exemplo, uma tarefa cara que não tem nada
	// para fazer logo a seguir a um arranque limpo).
	SkipInitial bool
}

// Runner corre um conjunto de tarefas até o contexto terminar.
type Runner struct {
	jobs []Job
	// Log recebe o que corre mal. Nil usa o registador por omissão.
	Log *slog.Logger

	// emCurso conta as tarefas a correr, para [Runner.Wait] saber por quem
	// espera.
	emCurso sync.WaitGroup
}

// NewRunner cria o executor.
func NewRunner(jobs ...Job) *Runner { return &Runner{jobs: jobs} }

// Add acrescenta tarefas.
func (r *Runner) Add(jobs ...Job) *Runner {
	r.jobs = append(r.jobs, jobs...)
	return r
}

// Start arranca todas as tarefas em segundo plano e devolve de imediato.
//
// As tarefas param quando o contexto terminar. Cancele-o no encerramento do
// serviço e dê-lhes tempo de acabar a execução em curso: interromper uma
// confirmação a meio deixa um pagamento cobrado no gateway e por confirmar do
// nosso lado.
func (r *Runner) Start(ctx context.Context) {
	for _, job := range r.jobs {
		r.emCurso.Add(1)
		go func(job Job) {
			defer r.emCurso.Done()
			r.run(ctx, job)
		}(job)
	}
}

// Wait espera que as tarefas em curso acabem, até ao prazo dado.
//
// Devolve falso se o prazo passou com trabalho ainda a correr, para quem chama
// poder registar que encerrou à força. Chame-o depois de cancelar o contexto,
// no encerramento do serviço: fechar a base de dados debaixo de uma tarefa a
// meio deixa um pagamento cobrado no gateway e por confirmar do nosso lado, que
// é o género de coisa que alguém tem de ir resolver à mão.
func (r *Runner) Wait(timeout time.Duration) bool {
	acabou := make(chan struct{})
	go func() {
		r.emCurso.Wait()
		close(acabou)
	}()

	if timeout <= 0 {
		<-acabou
		return true
	}
	select {
	case <-acabou:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (r *Runner) run(ctx context.Context, job Job) {
	interval := job.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	timeout := job.Timeout
	if timeout <= 0 {
		timeout = interval
	}

	if !job.SkipInitial {
		r.once(ctx, job, timeout)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.log().Info("fllex: tarefa terminada", "job", job.Name)
			return
		case <-ticker.C:
			r.once(ctx, job, timeout)
		}
	}
}

// once corre a tarefa uma vez, com timeout e a recuperar de um pânico.
//
// O pânico é registado com a pilha e engolido de propósito: uma linha
// corrompida numa passagem não pode derrubar o processo que trata de todas as
// outras.
func (r *Runner) once(ctx context.Context, job Job, timeout time.Duration) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log().Error("fllex: pânico numa tarefa periódica",
				"job", job.Name, "panic", rec, "stack", string(debug.Stack()))
		}
	}()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	job.Run(runCtx)
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
