package examples_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/spolly-ao/fllex/entitlement"
)

// Os limites de um plano. Um recurso que não esteja no mapa vale zero, e não
// infinito: um limite esquecido na configuração de um plano novo bloqueia a
// criação, que se nota logo, em vez de abrir a porta, que só se nota na factura.
func Example_limitesDoPlano() {
	limites := entitlement.Limits{"utilizadores": 5, "projectos": entitlement.Unlimited}

	fmt.Println("utilizadores:", limites.Limit("utilizadores"))
	fmt.Println("projectos sem tecto:", limites.IsUnlimited("projectos"))
	fmt.Println("recurso esquecido:", limites.Limit("ficheiros"))

	// Output:
	// utilizadores: 5
	// projectos sem tecto: true
	// recurso esquecido: 0
}

// Descer de plano não apaga dados: os recursos acima do limite ficam suspensos,
// e voltam sozinhos se o plano subir. Suspende-se do mais recente para o mais
// antigo, que é a única ordem que se explica ao cliente numa frase.
func Example_descerDePlano() {
	ctx := context.Background()
	recursos := &recursosEmMemoria{itens: []entitlement.Resource{
		{ID: "user-1", CreatedAt: 1},
		{ID: "user-2", CreatedAt: 2},
		{ID: "user-3", CreatedAt: 3},
	}}

	plano := entitlement.Limits{"utilizadores": 3}
	guarda := entitlement.NewEnforcer(
		entitlement.ResolverFunc(func(context.Context, string) (entitlement.Entitlements, error) {
			return entitlement.Entitlements{Limits: plano}, nil
		}),
		recursos,
	)
	guarda.Log = slog.New(slog.NewTextHandler(io.Discard, nil)) // o exemplo não precisa do registo

	// Desce para o plano de dois utilizadores.
	plano["utilizadores"] = 2
	r, _ := guarda.Apply(ctx, "cliente-1")
	fmt.Println("suspensos:", r.Suspended)

	// Volta a subir: os suspensos regressam.
	plano["utilizadores"] = 5
	r, _ = guarda.Apply(ctx, "cliente-1")
	fmt.Println("repostos: ", r.Restored)

	// Output:
	// suspensos: [user-3]
	// repostos:  [user-3]
}

// recursosEmMemoria é o mínimo que o guarda precisa de saber: quais são os
// recursos e como se marcam. No seu projecto, duas consultas.
type recursosEmMemoria struct{ itens []entitlement.Resource }

func (r *recursosEmMemoria) List(context.Context, string, string) ([]entitlement.Resource, error) {
	return r.itens, nil
}

func (r *recursosEmMemoria) SetSuspended(_ context.Context, ids []string, suspenso bool) error {
	for i := range r.itens {
		for _, id := range ids {
			if r.itens[i].ID == id {
				r.itens[i].Suspended = suspenso
			}
		}
	}
	return nil
}
