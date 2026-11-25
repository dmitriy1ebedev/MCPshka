package memory

// Owner vocabulary of recall (0.11.3 exam): the owner writes «мспишка»,
// «лиз», «фейс айди», records say MCPshka, lease, Face ID. A query gets the
// record spelling added; stop words leave the word indexes so «что делать»
// does not pull every record with a long question list.

import "strings"

// vocab: owner form → record form; «x*» is a word prefix, «a b» a phrase.
var vocab = []struct{ from, to string }{
	{"мспишк*|эмсипишк*", "mcpshka"},
	{"лейн*", "lane"},
	{"диаг*", "diag"},
	{"кодекс*", "codex"},
	{"клод*", "claude"},
	{"кими", "kimi"},
	{"гит|гита|гиту|гитом", "git"},
	{"фейс айди*|фейсайди*", "face id"},
	{"лиз|лиза|лизу|лизом|лизы", "lease"},
	{"впс*", "vps"},
	{"мсп|мцп", "mcp"},
	{"чатгпт*|чат гпт", "chatgpt"},
	{"гитверс*", "gitverse"},
	{"релиз*", "release"},
	{"тв|телевизор*|телек*", "tv"},
	{"ха|хоум ассистант*", "home assistant"},
	{"приставк*", "h96"},
	{"ии", "ai"},
	{"докер*", "docker"},
	{"сборк*", "build"},
	{"почт*|email*|мейл*", "email"},
	{"зачем", "цель"},
	{"перезагруз*|ребут*", "reboot"},
}

// expand prepends the record spelling of owner words found in text.
func expand(text string) string {
	j := " " + strings.Join(tokens(strings.ReplaceAll(text, "ё", "е")), " ") + " "
	add := []string{}
	for _, v := range vocab {
		for _, p := range strings.Split(v.from, "|") {
			if pre, ok := strings.CutSuffix(p, "*"); ok && strings.Contains(j, " "+pre) || strings.Contains(j, " "+p+" ") {
				add = append(add, v.to)
				break
			}
		}
	}
	if len(add) == 0 {
		return text
	}
	return strings.Join(add, " ") + " " + text
}

// stopWords leave the word and form indexes (the meaning layer weighs them down itself).
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "be": true, "do": true, "does": true, "for": true,
	"how": true, "in": true, "is": true, "of": true, "on": true, "or": true, "the": true, "to": true,
	"what": true, "when": true, "which": true, "why": true, "with": true, "а": true, "без": true, "более": true,
	"бы": true, "был": true, "была": true, "были": true, "было": true, "быть": true, "в": true, "вам": true,
	"вас": true, "весь": true, "во": true, "вообще": true, "вот": true, "все": true, "всего": true,
	"всех": true, "всё": true, "вы": true, "где": true, "да": true, "даже": true, "делаем": true,
	"делать": true, "для": true, "до": true, "должен": true, "должна": true, "должно": true, "должны": true,
	"его": true, "ее": true, "если": true, "есть": true, "еще": true, "ещё": true, "её": true, "же": true,
	"за": true, "зачем": true, "здесь": true, "и": true, "из": true, "или": true, "им": true, "итоге": true,
	"их": true, "к": true, "как": true, "какая": true, "какие": true, "каким": true, "какой": true,
	"какую": true, "ко": true, "когда": true, "кто": true, "куда": true, "ли": true, "либо": true, "мне": true,
	"может": true, "можно": true, "мы": true, "на": true, "надо": true, "наш": true, "не": true, "него": true,
	"нее": true, "нельзя": true, "нет": true, "неё": true, "ни": true, "них": true, "но": true, "ну": true,
	"нужен": true, "нужна": true, "нужно": true, "о": true, "об": true, "однако": true, "он": true, "она": true,
	"они": true, "оно": true, "от": true, "откуда": true, "очень": true, "по": true, "под": true,
	"почему": true, "при": true, "с": true, "сделать": true, "сейчас": true, "сколько": true, "со": true,
	"так": true, "также": true, "такой": true, "там": true, "те": true, "тем": true, "то": true, "того": true,
	"тоже": true, "той": true, "только": true, "том": true, "ты": true, "у": true, "уже": true, "хотя": true,
	"чего": true, "чей": true, "чем": true, "что": true, "что-то": true, "чтобы": true, "чье": true,
	"чья": true, "эта": true, "эти": true, "это": true, "я": true,
}
