package phone

import (
	"errors"
	"testing"
)

func TestNormalizeAO(t *testing.T) {
	tests := []struct{ in, want string }{
		{"+244 921 234 567", "244921234567"},
		{"00244921234567", "244921234567"},
		{"921 234 567", "244921234567"},
		{"921234567", "244921234567"},
		{"(244) 921-234-567", "244921234567"},
		{"244921234567", "244921234567"},
	}
	for _, tt := range tests {
		if got := NormalizeAO(tt.in); got != tt.want {
			t.Errorf("NormalizeAO(%q) = %q, queria %q", tt.in, got, tt.want)
		}
	}
}

func TestValidAO(t *testing.T) {
	valid := []string{"+244 921 234 567", "921234567", "244923456789"}
	for _, v := range valid {
		if !ValidAO(v) {
			t.Errorf("ValidAO(%q) = false, queria true", v)
		}
	}
	// Um fixo (o 2 a seguir ao indicativo), um número curto e um estrangeiro
	// não servem para o Multicaixa Express.
	invalid := []string{"244222334455", "12345", "+351912345678", "", "abc"}
	for _, v := range invalid {
		if ValidAO(v) {
			t.Errorf("ValidAO(%q) = true, queria false", v)
		}
	}
}

func TestCheckAO(t *testing.T) {
	got, err := CheckAO("+244 921 234 567")
	if err != nil {
		t.Fatalf("CheckAO devolveu erro: %v", err)
	}
	if got != "244921234567" {
		t.Errorf("CheckAO = %q, queria %q", got, "244921234567")
	}
	if _, err := CheckAO("12345"); !errors.Is(err, ErrInvalidAO) {
		t.Errorf("CheckAO de número inválido devolveu %v, queria ErrInvalidAO", err)
	}
}

func TestFormatAODDS(t *testing.T) {
	if got := FormatAODDS("921234567"); got != "+244-921234567" {
		t.Errorf("FormatAODDS = %q, queria %q", got, "+244-921234567")
	}
	// Um número inválido devolve vazio: enviar lixo faz o registo do mandato
	// falhar sem explicação.
	if got := FormatAODDS("12345"); got != "" {
		t.Errorf("FormatAODDS de número inválido = %q, queria vazio", got)
	}
}

func TestSameAO(t *testing.T) {
	if !SameAO("244923456789", "923456789") {
		t.Error("números com e sem indicativo deviam ser o mesmo")
	}
	if !SameAO("+244 923 456 789", "923-456-789") {
		t.Error("formatação diferente não devia contar")
	}
	if SameAO("923456789", "923456780") {
		t.Error("números diferentes não deviam corresponder")
	}
	if SameAO("", "923456789") {
		t.Error("um número vazio não corresponde a nada")
	}
}

func TestSameAOWithIdenticalDigits(t *testing.T) {
	// O caminho curto: dígitos iguais sem precisar de tirar o indicativo.
	if !SameAO("244923456789", "+244 923 456 789") {
		t.Error("os mesmos dígitos deviam corresponder")
	}
	if !SameAO("923456789", "923456789") {
		t.Error("números idênticos sem indicativo")
	}
}

func TestDigits(t *testing.T) {
	if got := Digits("+244 (923) 456-789"); got != "244923456789" {
		t.Errorf("= %q", got)
	}
	if got := Digits("sem números"); got != "" {
		t.Errorf("= %q", got)
	}
}
