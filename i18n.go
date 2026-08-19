package main

// Minimal i18n: templates call {{t "English text"}} / {{tf "format %s" args}};
// the English string is the key, unknown keys fall back to themselves. The
// language is an app-wide setting (single-user app), held atomically so a
// settings change applies without restart.

import (
	"encoding/json"
	"fmt"
	"html/template"
)

var languages = []struct{ Code, Name string }{
	{"en", "English"},
	{"pt-BR", "Português (Brasil)"},
}

func validLang(code string) bool {
	for _, l := range languages {
		if l.Code == code {
			return true
		}
	}
	return false
}

var themes = []string{"auto", "light", "dark"}

func validTheme(t string) bool {
	for _, v := range themes {
		if v == t {
			return true
		}
	}
	return false
}

const (
	defaultLang  = "en"
	defaultTheme = "dark"
)

// translateIn looks a string up in one language; unknown keys fall back to
// the English text, which is the key itself.
func translateIn(lang, key string) string {
	if m, ok := translations[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return key
}

// translate uses the language of the account behind the request; signed-out
// pages get the default.
func (s *server) translate(key string) string {
	return translateIn(defaultLang, key)
}

func (s *server) translatef(format string, args ...any) string {
	return fmt.Sprintf(s.translate(format), args...)
}

var translations = map[string]map[string]string{
	"pt-BR": {
		// sidebar / layout
		"+ Note":           "+ Nota",
		"+ Folder":         "+ Pasta",
		"Settings":         "Configurações",
		"Log out":          "Sair",
		"New note here":    "Nova nota aqui",
		"New subfolder":    "Nova subpasta",
		"Rename folder":    "Renomear pasta",
		"Delete folder":    "Excluir pasta",
		"Note title":       "Título da nota",
		"Collapse sidebar": "Recolher barra lateral",
		"Drag to resize":   "Arraste para redimensionar",
		"Folder name":      "Nome da pasta",
		"New note title":   "Novo título da nota",
		"New folder name":  "Novo nome da pasta",
		"Delete this folder and everything inside it?": "Excluir esta pasta e todo o seu conteúdo?",
		"Delete this note and all its chapters?":       "Excluir esta nota e todos os seus capítulos?",

		// index
		"Your definitive knowledge base, for any subject.":                                                                                                     "Sua base de conhecimento definitiva, para qualquer assunto.",
		"Create <strong>folders</strong> and <strong>notes</strong> from the sidebar.":                                                                         "Crie <strong>pastas</strong> e <strong>notas</strong> na barra lateral.",
		"Notes are split into <strong>chapters</strong>, each written in Markdown.":                                                                            "As notas são divididas em <strong>capítulos</strong>, cada um escrito em Markdown.",
		"Write math with <code>$…$</code> and <code>$$…$$</code>, rendered like LaTeX.":                                                                        "Escreva fórmulas com <code>$…$</code> e <code>$$…$$</code>, renderizadas como LaTeX.",
		"See the <a href=\"https://katex.org/docs/supported\" target=\"_blank\" rel=\"noopener\">KaTeX reference</a> for every supported function and symbol.": "Veja a <a href=\"https://katex.org/docs/supported\" target=\"_blank\" rel=\"noopener\">referência do KaTeX</a> com todas as funções e símbolos suportados.",
		"Number an equation with <code>\\label{name}</code> inside <code>$$…$$</code>, then reference it with <code>\\eqref{name}</code>, it links to the equation. References work within a chapter on the web, and across the whole note in the exported PDF.": "Numere uma equação com <code>\\label{nome}</code> dentro de <code>$$…$$</code> e referencie-a com <code>\\eqref{nome}</code>, vira um link para a equação. Referências funcionam dentro de um capítulo na web e em toda a nota no PDF exportado.",
		"Code blocks get syntax highlighting; <strong>Python</strong> and <strong>JavaScript</strong> blocks run in your browser.":                                                                                                                               "Blocos de código têm destaque de sintaxe; blocos <strong>Python</strong> e <strong>JavaScript</strong> rodam no seu navegador.",
		"Export any note, all chapters, as a LaTeX-typeset <strong>PDF</strong>.":                                                                                                                                                                                "Exporte qualquer nota, todos os capítulos, como <strong>PDF</strong> tipografado em LaTeX.",
		"Python snippets are unavailable: the browser runtime has not been fetched on this server.":                                                                                                                                                              "Trechos em Python indisponíveis: o runtime do navegador não foi baixado neste servidor.",
		"PDF export disabled: run `make typst` to fetch the typesetter.":                                                                                                                                                                                         "Exportação de PDF desativada: execute `make typst` para obter o compositor.",

		// note page
		"Export PDF":         "Exportar PDF",
		"Rename":             "Renomear",
		"Delete":             "Excluir",
		"Chapters":           "Capítulos",
		"New chapter title…": "Título do novo capítulo…",
		"Add chapter":        "Adicionar capítulo",
		"Images":             "Imagens",
		"Upload image":       "Enviar imagem",
		"Copy Markdown":      "Copiar Markdown",
		"Delete this image? Notes that reference it will show a broken link.":                                        "Excluir esta imagem? Notas que a referenciam mostrarão um link quebrado.",
		"Python snippets need the browser runtime, run `make pyodide` on the server. JavaScript snippets still run.": "Trechos em Python precisam do runtime do navegador, rode `make pyodide` no servidor. Trechos em JavaScript continuam funcionando.",
		"No chapters yet, add one below.": "Nenhum capítulo ainda, adicione um abaixo.",
		"Move up":                         "Mover para cima",
		"Move down":                       "Mover para baixo",
		"Delete chapter":                  "Excluir capítulo",
		"Delete chapter “%s”?":            "Excluir o capítulo “%s”?",

		// metadata
		"Edit metadata":                   "Editar metadados",
		"Type":                            "Tipo",
		"Description":                     "Descrição",
		"Short description of this note…": "Breve descrição desta nota…",

		// chapter page
		"Edit":             "Editar",
		"Save":             "Salvar",
		"Cancel":           "Cancelar",
		"Chapter %d of %d": "Capítulo %d de %d",
		"Write Markdown here… $math$, ```code fences```, tables, lists.": "Escreva Markdown aqui… $fórmulas$, ```blocos de código```, tabelas, listas.",

		// editor toolbar
		"Bold":          "Negrito",
		"Italic":        "Itálico",
		"Strikethrough": "Tachado",
		"Inline code":   "Código inline",
		"Code block":    "Bloco de código",
		"Heading":       "Título",
		"List":          "Lista",
		"Math":          "Fórmula",
		"Link":          "Link",
		"Image":         "Imagem",

		"in your browser": "no seu navegador",
		"JavaScript only, Python runtime not installed": "apenas JavaScript, runtime do Python não instalado",
		"Everything you write is encrypted before it is sent. What remains visible is the shape of the database, not its contents:": "Tudo o que você escreve é criptografado antes de ser enviado. O que continua visível é o formato do banco de dados, não o conteúdo:",
		"Note, chapter and folder names":               "Nomes de notas, capítulos e pastas",
		"no, the addresses of your pages are random":   "não, os endereços das suas páginas são aleatórios",
		"Descriptions, metadata fields and note types": "Descrições, campos de metadados e tipos de nota",
		"no":                             "não",
		"Code snippets and their output": "Trechos de código e suas saídas",
		"no, they run in your browser and are never sent":                "não, eles rodam no seu navegador e nunca são enviados",
		"How many notes and folders you have, and their sizes and dates": "Quantas notas e pastas você tem, com tamanhos e datas",
		"yes, the database has to be organised somehow":                  "sim, o banco precisa ser organizado de alguma forma",
		"During PDF export": "Durante a exportação em PDF",
		"Someone with the database can therefore see how large your knowledge base is and when you work on it, but not what any of it says.":                             "Quem tiver o banco de dados consegue ver o tamanho da sua base e quando você trabalha nela, mas não o que ela diz.",
		"Titles, folders, images and metadata are encrypted too. The server can see how much you have written and when, but not what it says.":                           "Títulos, pastas, imagens e metadados também são criptografados. O servidor vê quanto você escreveu e quando, mas não o que está escrito.",
		"Titles, folder names, images and metadata are encrypted with the same key. The server can see how much you have written and when, but not what any of it says.": "Títulos, nomes de pastas, imagens e metadados são criptografados com a mesma chave. O servidor vê quanto você escreveu e quando, mas não o que está escrito.",
		// password strength
		"Too short": "Curta demais",
		"Use at least 12 characters, a few unrelated words is the easiest way.": "Use pelo menos 12 caracteres, algumas palavras sem relação entre si é o jeito mais fácil.",
		"Could not check strength": "Não foi possível avaliar a força",
		"The strength estimator did not load. A long passphrase is still your best move.": "O avaliador de força não carregou. Uma frase-senha longa continua sendo a melhor escolha.",
		"Very weak":   "Muito fraca",
		"Weak":        "Fraca",
		"Fair":        "Razoável",
		"Strong":      "Forte",
		"Very strong": "Muito forte",
		"Guessing this offline would take about ":       "Adivinhar isso offline levaria cerca de ",
		"Choose a stronger password before continuing.": "Escolha uma senha mais forte antes de continuar.",
		"Length is what matters, not punctuation. Four or five unrelated words make a password that is easy to remember and slow to guess; a short one with symbols in it is neither.": "O que importa é o comprimento, não a pontuação. Quatro ou cinco palavras sem relação entre si formam uma senha fácil de lembrar e lenta de adivinhar; uma curta cheia de símbolos não é nem uma coisa nem outra.",

		// device PIN
		"Device PIN": "PIN do dispositivo",
		"A six-digit PIN unlocks this browser instead of your full password. It is stored nowhere: what is kept on this device is your key sealed with the PIN, and it never reaches the server.": "Um PIN de seis dígitos desbloqueia este navegador no lugar da senha completa. Ele não é guardado em lugar nenhum: o que fica neste dispositivo é sua chave lacrada com o PIN, e ele nunca chega ao servidor.",
		"New PIN":                   "Novo PIN",
		"Repeat PIN":                "Repita o PIN",
		"Lock after":                "Bloquear após",
		"1 minute":                  "1 minuto",
		"5 minutes":                 "5 minutos",
		"15 minutes":                "15 minutos",
		"30 minutes":                "30 minutos",
		"1 hour":                    "1 hora",
		"Never (only on reopening)": "Nunca (apenas ao reabrir)",
		"Set PIN":                   "Definir PIN",
		"Change PIN":                "Alterar PIN",
		"Lock now":                  "Bloquear agora",
		"Remove PIN":                "Remover PIN",
		"set on this device":        "definido neste dispositivo",
		"not set on this device":    "não definido neste dispositivo",
		"Six digits is a million combinations: enough to stop someone who picks up your unlocked laptop, not enough to stop someone who copies this browser's storage and guesses at their leisure. Ten wrong tries erase the sealed key from this device, and your password still gets you back in.": "Seis dígitos são um milhão de combinações: o bastante para deter quem pega seu computador destravado, mas não quem copia o armazenamento deste navegador e adivinha com calma. Dez tentativas erradas apagam a chave lacrada deste dispositivo, e sua senha continua te trazendo de volta.",
		"Locked":                                "Bloqueado",
		"Enter your PIN to unlock this device.": "Digite seu PIN para desbloquear este dispositivo.",
		"PIN":                                   "PIN",
		"Unlock":                                "Desbloquear",
		"Unlocking…":                            "Desbloqueando…",
		"The PIN is six digits.":                "O PIN tem seis dígitos.",
		"Wrong PIN. ":                           "PIN incorreto. ",
		"Attempts left: ":                       "Tentativas restantes: ",
		"Forgot your PIN? Sign in with your password": "Esqueceu o PIN? Entre com sua senha",
		"Unlock the app before setting a PIN.":        "Desbloqueie o app antes de definir um PIN.",
		"The two PINs do not match.":                  "Os dois PINs não coincidem.",
		"Your password":                               "Sua senha",
		"Confirms it is you before the lock changes.": "Confirma que é você antes de alterar o bloqueio.",
		"Enter your password to confirm.":             "Digite sua senha para confirmar.",
		"Your password, to confirm":                   "Sua senha, para confirmar",
		"Wrong password.":                             "Senha incorreta.",
		"Could not confirm your password.":            "Não foi possível confirmar sua senha.",
		"Could not reach the server.":                 "Não foi possível contatar o servidor.",

		// offline-first
		"Offline":                 "Sem conexão",
		"Syncing…":                "Sincronizando…",
		"Synced":                  "Sincronizado",
		"Sync now":                "Sincronizar agora",
		"Cannot reach the server": "Não foi possível alcançar o servidor",
		"%s waiting to sync":      "%s aguardando sincronização",
		"Available offline":       "Disponível sem conexão",
		"Your notes are always kept on this device. These two are large, so they are only downloaded if you ask.": "Suas notas ficam sempre neste dispositivo. Estes dois são grandes, então só são baixados se você pedir.",
		"Python for snippets":            "Python para trechos de código",
		"The typesetter, for PDF export": "O compositor, para exportar PDF",
		"Keep offline":                   "Manter sem conexão",
		"kept on this device":            "mantido neste dispositivo",
		"not kept":                       "não mantido",
		"not installed on the server":    "não instalado no servidor",
		"checking…":                      "verificando…",
		"Downloading…":                   "Baixando…",
		"Removing…":                      "Removendo…",
		"Remove":                         "Remover",
		"Unavailable offline — this is the one thing only the server knows.":                                                                      "Indisponível sem conexão — é a única coisa que só o servidor sabe.",
		"Shown from the last time this device was online.":                                                                                        "Mostrado a partir da última vez que este dispositivo esteve conectado.",
		"Everything here is encrypted in this browser and stored on this device first. The server gets a sealed copy when there is a connection.": "Tudo aqui é criptografado neste navegador e guardado primeiro neste dispositivo. O servidor recebe uma cópia lacrada quando há conexão.",
		"No notes yet. Use + Note to write the first one.":                                                                                        "Nenhuma nota ainda. Use + Nota para escrever a primeira.",
		"This address is not in this browser's copy. If it was written on another device, it will appear once this one syncs.":                    "Este endereço não está na cópia deste navegador. Se foi escrito em outro dispositivo, aparecerá quando este sincronizar.",
		"Recent":                               "Recentes",
		"Not found":                            "Não encontrado",
		"Move":                                 "Mover",
		"Move note":                            "Mover nota",
		"Folder":                               "Pasta",
		"(top level)":                          "(nível superior)",
		"Empty the folder before deleting it.": "Esvazie a pasta antes de excluí-la.",
		"When each note last changed":          "Quando cada nota mudou pela última vez",
		"yes — synchronising has to order changes somehow":                             "sim — a sincronização precisa ordenar as mudanças de alguma forma",
		"Notes stay here until you empty them. Emptying is permanent on every device.": "As notas ficam aqui até você esvaziar. Esvaziar é permanente em todos os dispositivos.",
		"The trash is empty.":                    "A lixeira está vazia.",
		"Delete forever":                         "Excluir para sempre",
		"Delete this note and everything in it?": "Excluir esta nota e tudo que há nela?",
		"Move this note to the trash?":           "Mover esta nota para a lixeira?",
		"Delete this chapter?":                   "Excluir este capítulo?",
		"Editing":                                "Editando",
		"Chapter %s of %s":                       "Capítulo %s de %s",
		"Loading…":                               "Carregando…",

		// rebuilt client views
		"%s notes":            "%s notas",
		"%s of %s":            "%s de %s",
		"Chapter title":       "Título do capítulo",
		"Code":                "Código",
		"Quote":               "Citação",
		"OK":                  "OK",
		"Note":                "Nota",
		"Trashed":             "Excluída em",
		"No chapters yet.":    "Nenhum capítulo ainda.",
		"Delete this folder?": "Excluir esta pasta?",
		"Delete this type?":   "Excluir este tipo?",
		"Markdown copied":     "Markdown copiado",
		"Create folders and notes from the sidebar.":                                                                                                    "Crie pastas e notas pela barra lateral.",
		"Notes are split into chapters, each written in Markdown.":                                                                                      "As notas são divididas em capítulos, cada um escrito em Markdown.",
		"Write math with $…$ and $$…$$ — rendered like LaTeX.":                                                                                          "Escreva matemática com $…$ e $$…$$ — renderizada como LaTeX.",
		"Everything is stored on this device first and works offline; the server only ever receives a sealed copy.":                                     "Tudo é guardado primeiro neste dispositivo e funciona sem conexão; o servidor só recebe uma cópia lacrada.",
		"Your definitive knowledge base — for any subject.":                                                                                             "Sua base de conhecimento definitiva — para qualquer assunto.",
		"Each note has a type. A type defines extra metadata fields — every note always has a title and a description.":                                 "Cada nota tem um tipo. Um tipo define campos extras de metadados — toda nota sempre tem título e descrição.",
		"Download everything as a zip: the Markdown source of every note, plus your images. Built here, from this device's copy, it works offline too.": "Baixe tudo como zip: o Markdown de cada nota, mais suas imagens. Montado aqui, da cópia deste dispositivo — funciona sem conexão.",
		"Unavailable offline, this is the one thing only the server knows.":                                                                             "Indisponível sem conexão — é a única coisa que só o servidor sabe.",
		"cannot delete: %s note(s) use this type":                                                                                                       "não é possível excluir: %s nota(s) usam este tipo",
		"no, both are built in your browser":                                                                                                            "não — os dois são gerados no seu navegador",
		"no, it never leaves this browser":                                                                                                              "não — ele nunca sai deste navegador",
		"Your PIN, if you set one":                                                                                                                      "Seu PIN, se você definir um",

		// password change
		"Change password":                                  "Alterar senha",
		"Changing…":                                        "Alterando…",
		"Current password":                                 "Senha atual",
		"New password":                                     "Nova senha",
		"Repeat new password":                              "Repita a nova senha",
		"The current password is wrong.":                   "A senha atual está incorreta.",
		"The password could not be changed.":               "Não foi possível alterar a senha.",
		"Unlock the app first.":                            "Desbloqueie o app primeiro.",
		"Password changed. Other devices were signed out.": "Senha alterada. Os outros dispositivos foram desconectados.",
		"Your notes are re-locked under the new password without being re-encrypted: the key that seals them never changes and never leaves this browser. Every other signed-in device is signed out.": "Suas notas passam a ser trancadas pela nova senha sem serem recriptografadas: a chave que as lacra nunca muda e nunca sai deste navegador. Todos os outros dispositivos conectados são desconectados.",
		"record limit reached (%d)": "limite de registros atingido (%d)",

		"Source code on GitHub": "Código-fonte no GitHub",
		"Source code":           "Código-fonte",

		"Signing out…": "Saindo…",

		// code / run
		"Run":                "Executar",
		"Running…":           "Executando…",
		"Starting Python…":   "Iniciando o Python…",
		"Loading libraries…": "Carregando bibliotecas…",
		"timed out":          "tempo esgotado",
		"error":              "erro",
		"(no output)":        "(sem saída)",

		// types page
		"Note types": "Tipos de nota",
		"Each note has a type. A type defines extra metadata fields, every note always has a title and a description.": "Cada nota tem um tipo. Um tipo define campos extras de metadados, toda nota sempre tem título e descrição.",
		"New type name…":   "Nome do novo tipo…",
		"New type name":    "Novo nome do tipo",
		"Create type":      "Criar tipo",
		"Delete type":      "Excluir tipo",
		"No extra fields.": "Sem campos extras.",
		"New field label…": "Nome do novo campo…",
		"Add field":        "Adicionar campo",
		"Field":            "Campo",
		"Data type":        "Tipo de dado",
		"Remove field":     "Remover campo",
		"%d note(s)":       "%d nota(s)",

		// settings
		"Preferences":       "Preferências",
		"Language":          "Idioma",
		"Color scheme":      "Esquema de cores",
		"Auto (system)":     "Automático (sistema)",
		"Light":             "Claro",
		"Dark":              "Escuro",
		"Save preferences":  "Salvar preferências",
		"Manage note types": "Gerenciar tipos de nota",
		"Define note types and their custom metadata fields.": "Defina tipos de nota e seus campos de metadados personalizados.",
		"Export": "Exportar",
		"Download the whole database as a zip: Markdown sources and typeset PDFs for every note, plus attached images.": "Baixe todo o banco de dados em um zip: fontes Markdown e PDFs tipografados de cada nota, além das imagens anexadas.",
		"Export everything (.zip)": "Exportar tudo (.zip)",
		"%d notes":                 "%d notas",
		"%d images":                "%d imagens",
		"Status":                   "Status",
		"Snippet execution":        "Execução de trechos",
		"PDF export (in-browser)":  "Exportação de PDF (no navegador)",
		"Contents":                 "Sumário",
		"available":                "disponível",
		"unavailable":              "indisponível",
		"Your password cannot be changed or recovered: it never reaches this server, and only it can unlock what you have written.": "Sua senha não pode ser alterada nem recuperada: ela nunca chega a este servidor, e só ela abre o que você escreveu.",
		// trash
		"Trash":   "Lixeira",
		"Restore": "Restaurar",
		"Deleted notes are kept for 60 days, then removed permanently.": "Notas excluídas ficam guardadas por 60 dias e depois são removidas permanentemente.",
		"deleted %s · %d day(s) left":                                   "excluída em %s · %d dia(s) restantes",
		"Permanently delete “%s”? This cannot be undone.":               "Excluir “%s” permanentemente? Isso não pode ser desfeito.",

		// errors (flash banners)
		"title must contain at least one letter or digit":                              "O título deve conter pelo menos uma letra ou número",
		"name must contain at least one letter or digit":                               "O nome deve conter pelo menos uma letra ou número",
		"a note with that (or a too similar) title already exists in this folder":      "Já existe uma nota com esse título (ou parecido demais) nesta pasta",
		"a chapter with that (or a too similar) title already exists in this note":     "Já existe um capítulo com esse título (ou parecido demais) nesta nota",
		"a folder with that (or a too similar) name already exists here":               "Já existe uma pasta com esse nome (ou parecido demais) aqui",
		"that folder already contains a note with this title":                          "Essa pasta já contém uma nota com este título",
		"cannot restore: a note with the same title already exists at the destination": "Não é possível restaurar: já existe uma nota com o mesmo título no destino",
		"a type with that name already exists":                                         "Já existe um tipo com esse nome",
		"a field with that name already exists":                                        "Já existe um campo com esse nome",
		"the default Note type cannot be deleted":                                      "O tipo padrão Nota não pode ser excluído",
		"cannot delete: %d note(s) use this type":                                      "Não é possível excluir: %d nota(s) usam este tipo",
		"too many fields":                         "Campos demais",
		"no image in request (or file too large)": "Nenhuma imagem na requisição (ou arquivo grande demais)",
		"failed to read upload":                   "Falha ao ler o envio",
		"Image too large: the limit is %d MiB.":   "Imagem grande demais: o limite é %d MiB.",
		"You have reached the limit of %d images. Delete one to upload another.": "Você atingiu o limite de %d imagens. Exclua uma para enviar outra.",
		"%d of %d images": "%d de %d imagens",
		"unsupported image format (png, jpeg, gif, webp)": "Formato de imagem não suportado (png, jpeg, gif, webp)",

		// landing page
		"Everything you know, written down properly.": "Tudo o que você sabe, escrito como deve ser.",
		"Sign-ups are closed on this server.":         "Os cadastros estão fechados neste servidor.",
		"Notes with structure":                        "Notas com estrutura",
		"Organize notes in folders and split each one into chapters you can write, reorder and read separately.": "Organize notas em pastas e divida cada uma em capítulos que você escreve, reordena e lê separadamente.",
		"Markdown and mathematics": "Markdown e matemática",
		"Write in Markdown, typeset formulas like LaTeX, number equations and reference them across a note.": "Escreva em Markdown, componha fórmulas como no LaTeX, numere equações e referencie-as ao longo da nota.",
		"Code that runs": "Código que roda",
		"Highlighted snippets in any language, and Python and JavaScript blocks you can execute in the browser, numpy, scipy, sympy and pandas included.": "Trechos destacados em qualquer linguagem, e blocos Python e JavaScript que você executa no navegador, com numpy, scipy, sympy e pandas.",
		"Yours to keep": "Seu para sempre",
		"Export a note as a typeset PDF, or the whole base as a zip of Markdown sources and PDFs. No lock-in.": "Exporte uma nota como PDF tipografado ou toda a base como um zip com fontes Markdown e PDFs. Sem aprisionamento.",

		"free software under the GNU GPL v3.": "software livre sob a GNU GPL v3.",

		"An offline-first, encrypted knowledge base for Markdown, mathematics and runnable code.": "Uma base de conhecimento offline-first e criptografada para Markdown, matemática e código executável.",
		"Your knowledge base. Offline, encrypted, yours.":                                         "Sua base de conhecimento. Offline, criptografada, sua.",
		"Markdown notes with LaTeX-grade mathematics and runnable code. It installs like an app, works with no connection, and encrypts everything in your browser, free and open source.": "Notas em Markdown com matemática no nível do LaTeX e código executável. Instala como um aplicativo, funciona sem conexão e criptografa tudo no seu navegador, software livre e de código aberto.",
		"Works offline.": "Funciona offline.",
		"Install it like an app and keep writing with no connection; your notes sync across your devices once you are back online.": "Instale como um aplicativo e continue escrevendo sem conexão; suas notas sincronizam entre seus dispositivos assim que você volta a ficar online.",
		"Markdown, mathematics and code.": "Markdown, matemática e código.",
		"Write in Markdown, typeset numbered equations like LaTeX, and run Python and JavaScript (numpy, scipy, sympy and pandas) right in the page.": "Escreva em Markdown, componha equações numeradas como no LaTeX e rode Python e JavaScript (numpy, scipy, sympy e pandas) na própria página.",
		"Free and open source.": "Livre e de código aberto.",
		"Licensed under the GNU GPL v3, read it, audit it, or run your own server.": "Licenciado sob a GNU GPL v3, leia, audite ou rode seu próprio servidor.",
		"What you write is encrypted in your browser with a key derived from your password. Everything that syncs between your devices, and everything the server stores, is ciphertext it has no key for. The trade-off is absolute: there is no password reset and no recovery, lose the password and the writing is gone.": "O que você escreve é criptografado no seu navegador com uma chave derivada da sua senha. Tudo que sincroniza entre seus dispositivos, e tudo que o servidor guarda, é texto cifrado do qual ele não tem a chave. O custo é absoluto: não há redefinição nem recuperação de senha, perdeu a senha, perdeu o que escreveu.",

		"Notes in folders, split into chapters. Markdown with real mathematics, code that runs, and a PDF at the end of it.": "Notas em pastas, divididas em capítulos. Markdown com matemática de verdade, código que roda e um PDF no fim.",
		"The eigenvalue problem for an observable reads":                                                                     "O problema de autovalores de um observável é",
		"Chapters, not walls of text.":                                                                                       "Capítulos, não paredes de texto.",
		"Each one its own page, reorderable, with its own URL.":                                                              "Cada um em sua página, reordenável e com URL própria.",
		"Mathematics that behaves.":                                                                                          "Matemática que se comporta.",
		"Numbered equations you can reference, typeset like LaTeX.":                                                          "Equações numeradas que você referencia, compostas como no LaTeX.",
		"Python in your browser.":                                                                                            "Python no seu navegador.",
		"numpy, scipy, sympy and pandas, the code never reaches the server.":                                                 "numpy, scipy, sympy e pandas, o código nunca chega ao servidor.",
		"Nothing locked in.":                                                                                                 "Nada preso.",
		"Export a typeset PDF, or the whole base as Markdown.":                                                               "Exporte um PDF tipografado ou toda a base em Markdown.",

		"In development: end-to-end encryption": "Em desenvolvimento: criptografia ponta a ponta",
		"Notes will be encrypted in your browser with a key derived from your password, so the server only ever stores ciphertext it cannot read. The trade-off is absolute: there is no password reset and no recovery, lose the password and the notes are gone.": "As notas serão criptografadas no seu navegador com uma chave derivada da sua senha, de modo que o servidor guarde apenas texto cifrado que não consegue ler. O custo é absoluto: não há redefinição nem recuperação de senha, perdeu a senha, perdeu as notas.",
		"Not active yet: today notes are stored unencrypted on the server.": "Ainda não está ativo: hoje as notas são guardadas sem criptografia no servidor.",
		"by": "por",

		"Plan and limits":                 "Plano e limites",
		"Your account is on the %s plan.": "Sua conta está no plano %s.",
		"Notes":                           "Notas",
		"Largest image":                   "Imagem maior",
		"Largest chapter":                 "Capítulo maior",
		"%d of %d":                        "%d de %d",
		"%d MiB":                          "%d MiB",
		"Typesetting…":                    "Compondo…",
		"Preparing…":                      "Preparando…",
		"The archive is built entirely in this browser: Markdown sources and your images, decrypted here and never sent anywhere. PDFs are not included, because typesetting would mean handing the text to the server.": "O arquivo é montado inteiramente neste navegador: fontes Markdown e suas imagens, descriptografadas aqui e nunca enviadas a lugar nenhum. PDFs não são incluídos, porque compor exigiria entregar o texto ao servidor.",
		"Download everything as a zip: the Markdown source of every note, plus your images.":                                                                                                                             "Baixe tudo em um zip: o Markdown de cada nota e suas imagens.",
		"Nothing to export yet.": "Nada para exportar ainda.",
		"You have reached the limit of %d notes. Delete one to make room.": "Você atingiu o limite de %d notas. Exclua uma para abrir espaço.",
		"This chapter is too large: the limit is %s.":                      "Este capítulo é grande demais: o limite é %s.",

		// what the server can read
		"What this server can read": "O que este servidor consegue ler",
		"Encryption covers the text of your chapters and nothing else. Be aware of what is left in the clear:": "A criptografia cobre o texto dos seus capítulos e nada mais. Saiba o que fica em claro:",
		"Stored":                                       "Guardado",
		"Readable by the server":                       "Legível pelo servidor",
		"Chapter text":                                 "Texto dos capítulos",
		"no, encrypted in your browser":                "não, criptografado no seu navegador",
		"Note and chapter titles":                      "Títulos de notas e capítulos",
		"yes, they form the addresses of your pages":   "sim, eles formam os endereços das suas páginas",
		"Folder names":                                 "Nomes das pastas",
		"yes":                                          "sim",
		"Note descriptions and metadata fields":        "Descrições e campos de metadados",
		"Images you upload":                            "Imagens enviadas",
		"Your email address":                           "Seu endereço de email",
		"yes, it is how you sign in and are contacted": "sim, é como você entra e é contatado",
		"During PDF or zip export":                     "Durante a exportação em PDF ou zip",
		"briefly, the text is sent back to be typeset, then wiped":                                                              "brevemente, o texto volta para ser composto e depois é apagado",
		"Someone with the database can therefore see how your knowledge base is organised, and read anything above marked yes.": "Quem tiver o banco de dados consegue ver como sua base está organizada e ler tudo que está marcado como sim.",
		"End-to-end encryption": "Criptografia ponta a ponta",
		"What you write is encrypted in your browser with a key derived from your password, and the server stores only ciphertext it has no key for. The trade-off is absolute: there is no password reset and no recovery, lose the password and the writing is gone.": "O que você escreve é criptografado no seu navegador com uma chave derivada da sua senha, e o servidor guarda apenas texto cifrado para o qual não tem chave. O custo é absoluto: não há redefinição nem recuperação de senha, perdeu a senha, perdeu o que escreveu.",
		"Titles, folder names and images stay readable to the server, because they address and illustrate your pages. Settings lists exactly what is and is not covered.":                                                                                               "Títulos, nomes de pastas e imagens continuam legíveis para o servidor, porque endereçam e ilustram suas páginas. As configurações listam exatamente o que é e o que não é coberto.",

		// registration / verification
		"Create an account":        "Criar uma conta",
		"Create account":           "Criar conta",
		"Create one":               "Crie uma",
		"Email":                    "Email",
		"No account yet?":          "Ainda não tem conta?",
		"Already have an account?": "Já tem uma conta?",
		"Back to sign in":          "Voltar para o login",
		"Send the link again":      "Enviar o link novamente",
		"I am not a robot":         "Não sou um robô",
		"Verifying…":               "Verificando…",
		"Verified":                 "Verificado",
		"Verification failed":      "Falha na verificação",
		"Check your inbox, we sent you a link to confirm your address.":                     "Verifique sua caixa de entrada, enviamos um link para confirmar seu endereço.",
		"The link is valid for 24 hours. Remember to look in your spam folder.":             "O link vale por 24 horas. Lembre-se de olhar na pasta de spam.",
		"Address confirmed, you can sign in now.":                                           "Endereço confirmado, agora você pode entrar.",
		"Confirm your email address before signing in.":                                     "Confirme seu endereço de email antes de entrar.",
		"This confirmation link is invalid or has expired. Sign up again to get a new one.": "Este link de confirmação é inválido ou expirou. Cadastre-se novamente para receber outro.",
		"captcha check failed, please try again":                                            "falha na verificação do captcha, tente novamente",
		"Username must be 3-32 characters: letters, digits, dot, dash or underscore.":       "O usuário deve ter de 3 a 32 caracteres: letras, números, ponto, hífen ou sublinhado.",
		"That email address does not look valid.":                                           "Esse endereço de email não parece válido.",
		"Password must be between %d and %d characters.":                                    "A senha deve ter entre %d e %d caracteres.",
		"That username is already taken.":                                                   "Esse nome de usuário já está em uso.",
		"Confirm password":                                                                  "Confirme a senha",
		"The two passwords do not match.":                                                   "As duas senhas não coincidem.",
		"What you write is encrypted with this password before it leaves your device. There is no recovery: if you lose the password, the writing is unreadable forever.": "O que você escreve é criptografado com esta senha antes de sair do seu dispositivo. Não há recuperação: se perder a senha, o texto fica ilegível para sempre.",
		"Titles, folder names and images are not encrypted, settings explains exactly what the server can read.":                                                          "Títulos, nomes de pastas e imagens não são criptografados, as configurações explicam exatamente o que o servidor consegue ler.",
		"JavaScript is required: your keys are derived in the browser.":                                                                                                   "JavaScript é necessário: suas chaves são derivadas no navegador.",
		"Locked, sign in again to read this.": "Bloqueado, entre novamente para ler isto.",
		"Locked, sign in again to save.":      "Bloqueado, entre novamente para salvar.",
		"This account is missing its encryption keys and cannot be used. Please sign up again.": "Esta conta está sem suas chaves de criptografia e não pode ser usada. Cadastre-se novamente.",
		"Your browser could not prepare the encryption keys. Enable JavaScript and try again.":  "Seu navegador não conseguiu preparar as chaves de criptografia. Ative o JavaScript e tente novamente.",
		"We could not send the confirmation email. Please try again later.":                     "Não foi possível enviar o email de confirmação. Tente novamente mais tarde.",

		// login
		"Sign in":                     "Entrar",
		"Username":                    "Usuário",
		"Password":                    "Senha",
		"Wrong username or password.": "Usuário ou senha incorretos.",
		"Too many attempts. Try again in %d minute(s).":                   "Tentativas demais. Tente novamente em %d minuto(s).",
		"There are no accounts yet and sign-up is closed on this server.": "Ainda não há contas e os cadastros estão fechados neste servidor.",
		"Configure mail and set this to open registration:":               "Configure o email e defina isto para abrir os cadastros:",
	},
}

// clientStrings hands the browser the few strings it composes on its own —
// snippet results, which the server never sees and so cannot render. Anything
// not listed here falls back to the English key in ngT().
var clientKeys = []string{
	"Run", "Running…", "Starting Python…", "Loading libraries…",
	"timed out", "error", "(no output)",
	// the typeset PDF is assembled in the browser too
	"Contents",
	// password strength
	"Too short", "Use at least 12 characters, a few unrelated words is the easiest way.",
	"Could not check strength",
	"The strength estimator did not load. A long passphrase is still your best move.",
	"Very weak", "Weak", "Fair", "Strong", "Very strong",
	"Guessing this offline would take about ", "Close. One more word would put this out of reach.",
	// the lock screen and its settings
	"Locked", "Enter your PIN to unlock this device.", "PIN", "Unlock", "Unlocking…",
	"The PIN is six digits.", "Wrong PIN. ", "Attempts left: ",
	"Forgot your PIN? Sign in with your password",
	"Unlock the app before setting a PIN.", "The two PINs do not match.",
	"set on this device", "not set on this device", "Set PIN", "Change PIN",
}

func clientStrings(lang string) template.JS {
	out := make(map[string]string, len(clientKeys))
	for _, key := range clientKeys {
		out[key] = translateIn(lang, key)
	}
	// into a data attribute, so it needs to survive HTML escaping intact
	blob, err := json.Marshal(out)
	if err != nil {
		return template.JS("{}")
	}
	return template.JS(blob)
}
