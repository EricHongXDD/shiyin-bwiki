package bwiki

import (
	stdhtml "html"
	"net/url"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	xhtml "golang.org/x/net/html"
)

var (
	zhFilePattern = regexp.MustCompile(`(?i)(?:^|[^a-z])(zh(?:-cn)?|cn|chs|sc)(?:[^a-z]|$)`)
	jaFilePattern = regexp.MustCompile(`(?i)(?:^|[^a-z])(ja|jp|jpn)(?:[^a-z]|$)`)
	enFilePattern = regexp.MustCompile(`(?i)(?:^|[^a-z])(en|eng)(?:[^a-z]|$)`)
)

type audioCandidate struct {
	node     *xhtml.Node
	row      *xhtml.Node
	table    *xhtml.Node
	cell     *xhtml.Node
	fileName string
	url      string
	language Language
}

type scanStats struct {
	markers int
	invalid int
}

type candidateDedupKey struct {
	scope *xhtml.Node
	url   string
}

func parseHTMLPage(markup string, target Target, displayTitle string) (Page, error) {
	document, err := xhtml.Parse(strings.NewReader(markup))
	if err != nil {
		return Page{}, parseFailure(KindStructure, "parseHTML", target.SourceURL, "页面 HTML 无法解析", 0, err)
	}
	stats := &scanStats{}
	candidates := extractCandidates(document, target, stats)
	sections := candidateSections(document, candidates)
	entries, seen := parseTables(document, candidates, sections)
	entries = appendGenericEntries(entries, candidates, sections, seen)
	entries = compactEntries(entries)
	if len(entries) == 0 {
		if stats.markers > 0 {
			return Page{}, parseFailure(KindStructure, "parseHTML", target.SourceURL,
				"页面包含音频标记，但其结构或地址无法识别，BWIKI 页面结构可能已变化", 0, nil)
		}
		return Page{}, parseFailure(KindNoAudio, "parseHTML", target.SourceURL, "页面中没有发现可下载的语音", 0, nil)
	}
	if displayTitle == "" {
		displayTitle = extractDisplayTitle(document)
	} else {
		displayTitle = stripHTML(displayTitle)
	}
	if displayTitle == "" {
		displayTitle = target.PageTitle
	}
	warnings := make([]string, 0, 1)
	if stats.invalid > 0 {
		warnings = append(warnings, "已跳过 "+strconv.Itoa(stats.invalid)+" 个无效或不受信任的音频标记。")
	}
	return Page{
		SourceURL:    target.SourceURL,
		WikiBaseURL:  target.WikiBaseURL,
		PageTitle:    target.PageTitle,
		DisplayTitle: cleanTextValue(displayTitle),
		Languages:    collectLanguages(entries),
		Entries:      entries,
		Warnings:     warnings,
	}, nil
}

// extractCandidates 按 data-file、audio/src、a[href] 的优先级提取候选项。
func extractCandidates(root *xhtml.Node, target Target, stats *scanStats) []*audioCandidate {
	var result []*audioCandidate
	seen := make(map[candidateDedupKey]struct{})
	successfulDataFiles := make(map[*xhtml.Node]bool)
	successfulSources := make(map[*xhtml.Node]bool)

	// 第一轮同时统计结构标记，便于区分“无音频”和“结构变化”。
	walkNodes(root, func(node *xhtml.Node) {
		if node.Type != xhtml.ElementNode {
			return
		}
		if _, ok := attribute(node, "data-file"); ok || node.Data == "audio" || node.Data == "source" || hasAudioClass(node) {
			stats.markers++
		}
	})

	add := func(node *xhtml.Node, raw string) bool {
		fileName, audioURL, err := resolveAudioReference(raw, node, target)
		if err != nil {
			stats.invalid++
			return false
		}
		key := candidateDedupKey{scope: candidateDedupScope(node), url: audioURL}
		if _, exists := seen[key]; exists {
			return true
		}
		seen[key] = struct{}{}
		result = append(result, &audioCandidate{
			node: node, row: nearestElement(node, "tr"), table: nearestElement(node, "table"),
			cell: nearestCell(node), fileName: fileName, url: audioURL,
			language: languageFromFileName(fileName),
		})
		return true
	}

	// data-file 是 BWIKI AudioPlayer 模板保留的最可靠来源。
	walkNodes(root, func(node *xhtml.Node) {
		if node.Type != xhtml.ElementNode {
			return
		}
		if raw, ok := attribute(node, "data-file"); ok {
			successfulDataFiles[node] = add(node, raw)
		}
	})
	// 页面若已经渲染出 audio/source，则使用其真实 src，但不重复处理 data-file 子树。
	walkNodes(root, func(node *xhtml.Node) {
		if node.Type != xhtml.ElementNode || node.Data != "audio" && node.Data != "source" || hasSuccessfulAncestor(node, successfulDataFiles) {
			return
		}
		if raw, ok := attribute(node, "src"); ok {
			successfulSources[node] = add(node, raw)
		}
	})
	// 最后兼容普通媒体链接和 MediaWiki 文件链接。
	walkNodes(root, func(node *xhtml.Node) {
		if node.Type != xhtml.ElementNode || node.Data != "a" ||
			hasSuccessfulAncestor(node, successfulDataFiles) || hasSuccessfulAncestor(node, successfulSources) ||
			anchorClaimedByDataFile(node, successfulDataFiles) {
			return
		}
		href, ok := attribute(node, "href")
		if !ok {
			return
		}
		label := cleanFileLabel(nodeText(node))
		if !looksAudioReference(href) && !looksAudioReference(label) {
			return
		}
		stats.markers++
		add(node, href)
	})
	return result
}

// candidateDedupScope 只合并同一播放器容器或表格单元格内的重复渲染。
// 不同台词行即使复用同一个音频 URL，也必须保留为独立候选项。
func candidateDedupScope(node *xhtml.Node) *xhtml.Node {
	if cell := nearestCell(node); cell != nil {
		return cell
	}
	for current := node; current != nil; current = current.Parent {
		if current.Type != xhtml.ElementNode {
			continue
		}
		class, _ := attribute(current, "class")
		class = strings.ToLower(class)
		if current != node && (hasAudioClass(current) || strings.Contains(class, "voice-language") || strings.Contains(class, "ship_word_media_wrap")) {
			return current
		}
		switch current.Data {
		case "li", "p", "figure", "figcaption":
			return current
		}
	}
	return node
}

func parseTables(root *xhtml.Node, candidates []*audioCandidate, sections map[*audioCandidate]string) ([]Entry, map[*audioCandidate]struct{}) {
	byRow := make(map[*xhtml.Node][]*audioCandidate)
	for _, candidate := range candidates {
		if candidate.row != nil && candidate.table != nil {
			byRow[candidate.row] = append(byRow[candidate.row], candidate)
		}
	}
	var entries []Entry
	seen := make(map[*audioCandidate]struct{})
	walkSectionElements(root, func(table *xhtml.Node, section string) {
		if table.Data != "table" {
			return
		}
		rows := tableRows(table)
		activeEntry := -1
		remainingRows := 0
		for _, row := range rows {
			rowCandidates := filterTableCandidates(byRow[row], table)
			cells := directCells(row)
			titleCell, titleIndex := rowTitleCell(cells, rowCandidates)
			entryIndex := activeEntry
			if titleCell != nil {
				title := cleanTextWithoutAudio(titleCell)
				if title == "" && len(rowCandidates) > 0 {
					title = rowCandidates[0].fileName
				}
				entrySection := section
				if len(rowCandidates) > 0 {
					if candidateSection := sections[rowCandidates[0]]; candidateSection != "" {
						entrySection = candidateSection
					}
				}
				entries = append(entries, Entry{Section: entrySection, Title: title})
				entryIndex = len(entries) - 1
				activeEntry = entryIndex
				remainingRows = rowSpan(titleCell) - 1
			} else if len(rowCandidates) == 0 {
				if remainingRows > 0 {
					remainingRows--
				}
				continue
			} else if entryIndex < 0 || remainingRows <= 0 {
				entries = append(entries, Entry{Section: sections[rowCandidates[0]], Title: rowCandidates[0].fileName})
				entryIndex = len(entries) - 1
				activeEntry = entryIndex
				remainingRows = 0
			} else {
				remainingRows--
			}

			for _, candidate := range rowCandidates {
				if _, duplicate := seen[candidate]; duplicate {
					continue
				}
				text, textContainer := rowCandidateText(candidate, row, cells, titleIndex, rowCandidates)
				language := candidate.language
				if language == LanguageUnknown {
					language = languageFromAttributes(candidate.node, row)
				}
				if language == LanguageUnknown && textContainer != nil {
					language = languageFromAttributes(textContainer, row)
				}
				if language == LanguageUnknown {
					language = languageFromPresentation(candidate.cell, row)
				}
				if language == LanguageUnknown {
					language = languageFromText(text)
				}
				entries[entryIndex].Audios = append(entries[entryIndex].Audios, Audio{
					Language: language, Text: text, FileName: candidate.fileName, URL: candidate.url,
				})
				seen[candidate] = struct{}{}
			}
		}
	})
	return entries, seen
}

func appendGenericEntries(entries []Entry, candidates []*audioCandidate, sections map[*audioCandidate]string, seen map[*audioCandidate]struct{}) []Entry {
	containerEntries := make(map[*xhtml.Node]int)
	for _, candidate := range candidates {
		if _, exists := seen[candidate]; exists {
			continue
		}
		container := genericContainer(candidate.node)
		entryIndex, grouped := containerEntries[container]
		if !grouped {
			title := genericTitle(candidate, container)
			entries = append(entries, Entry{Section: sections[candidate], Title: title})
			entryIndex = len(entries) - 1
			containerEntries[container] = entryIndex
		}
		text := cleanTextWithoutAudio(container)
		if text == entries[entryIndex].Title || isNoiseText(text) {
			text = ""
		}
		language := candidate.language
		if language == LanguageUnknown {
			language = languageFromAttributes(candidate.node, container)
		}
		if language == LanguageUnknown {
			language = languageFromPresentation(candidate.cell, container)
		}
		if language == LanguageUnknown {
			language = languageFromText(text)
		}
		entries[entryIndex].Audios = append(entries[entryIndex].Audios, Audio{
			Language: language, Text: text, FileName: candidate.fileName, URL: candidate.url,
		})
		seen[candidate] = struct{}{}
	}
	return entries
}

func rowTitleCell(cells []*xhtml.Node, candidates []*audioCandidate) (*xhtml.Node, int) {
	firstAudio := len(cells)
	for _, candidate := range candidates {
		for index, cell := range cells {
			if candidate.cell == cell && index < firstAudio {
				firstAudio = index
			}
		}
	}
	if firstAudio == len(cells) {
		for index, cell := range cells {
			if cellHasAudioMarker(cell) {
				firstAudio = index
				break
			}
		}
	}
	if firstAudio == len(cells) {
		return nil, -1
	}
	for index := 0; index < firstAudio; index++ {
		if text := cleanTextWithoutAudio(cells[index]); text != "" {
			return cells[index], index
		}
	}
	return nil, -1
}

func cellHasAudioMarker(cell *xhtml.Node) bool {
	found := false
	walkNodes(cell, func(node *xhtml.Node) {
		if found || node.Type != xhtml.ElementNode {
			return
		}
		if _, ok := attribute(node, "data-file"); ok || node.Data == "audio" || node.Data == "source" || hasAudioClass(node) {
			found = true
			return
		}
		if node.Data == "a" {
			href, _ := attribute(node, "href")
			found = looksAudioReference(href) || looksAudioReference(cleanFileLabel(nodeText(node)))
		}
	})
	return found
}

func rowCandidateText(candidate *audioCandidate, row *xhtml.Node, cells []*xhtml.Node, titleIndex int, rowCandidates []*audioCandidate) (string, *xhtml.Node) {
	if container := languageTextContainer(candidate.node, row); container != nil {
		if text := cleanTextWithoutAudio(container); text != "" && !isNoiseText(text) {
			return text, container
		}
	}
	cellIndex := -1
	for index, cell := range cells {
		if candidate.cell == cell {
			cellIndex = index
			break
		}
	}
	if cellIndex >= 0 {
		if ownText := cleanTextWithoutAudio(cells[cellIndex]); ownText != "" && !isNoiseText(ownText) {
			return ownText, cells[cellIndex]
		}
		for index := cellIndex + 1; index < len(cells); index++ {
			if index == titleIndex || cellContainsCandidate(cells[index], rowCandidates) {
				continue
			}
			if text := cleanTextWithoutAudio(cells[index]); text != "" && !isNoiseText(text) {
				return text, cells[index]
			}
		}
	}
	return "", nil
}

func compactEntries(entries []Entry) []Entry {
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if len(entry.Audios) == 0 {
			continue
		}
		entry.Section = cleanTextValue(entry.Section)
		entry.Title = cleanEntryTitle(entry.Title)
		if entry.Title == "" {
			entry.Title = entry.Audios[0].FileName
		}
		result = append(result, entry)
	}
	return result
}

// cleanEntryTitle 去除 BWIKI 条目标题末尾混入的触发标记。
func cleanEntryTitle(value string) string {
	title := cleanTextValue(value)
	for index, character := range title {
		if character != ' ' && character != '\t' {
			continue
		}
		suffix := strings.TrimSpace(title[index:])
		if strings.Contains(suffix, "触发") {
			return strings.TrimSpace(title[:index])
		}
	}
	return title
}

func collectLanguages(entries []Entry) []Language {
	found := make(map[Language]bool)
	for _, entry := range entries {
		for _, audio := range entry.Audios {
			found[audio.Language] = true
		}
	}
	order := []Language{LanguageZH, LanguageJA, LanguageEN, LanguageUnknown}
	result := make([]Language, 0, len(order))
	for _, language := range order {
		if found[language] {
			result = append(result, language)
		}
	}
	return result
}

func resolveAudioReference(raw string, node *xhtml.Node, target Target) (string, string, error) {
	raw = strings.TrimSpace(stdhtml.UnescapeString(raw))
	if raw == "" {
		return "", "", invalidAudioReference(raw)
	}
	label := anchorFileLabel(node, raw)
	if label == "" {
		label = fileNameFromReference(raw)
	}
	label = cleanFileLabel(label)
	if label == "" || !looksAudioReference(label) {
		return "", "", invalidAudioReference(raw)
	}
	if fileName := mediaWikiFileName(raw); fileName != "" {
		if anchor := anchorFileLabel(node, raw); anchor != "" {
			fileName = cleanFileLabel(anchor)
		}
		return fileName, specialFileURL(target.WikiBaseURL, fileName), nil
	}
	if isBareFileName(raw) {
		return label, specialFileURL(target.WikiBaseURL, label), nil
	}
	if strings.HasPrefix(raw, "//") {
		base, _ := url.Parse(target.WikiBaseURL)
		raw = base.Scheme + ":" + raw
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	if !reference.IsAbs() {
		base, _ := url.Parse(target.WikiBaseURL + "/")
		reference = base.ResolveReference(reference)
	}
	if !isTrustedAudioURL(reference) {
		return "", "", invalidAudioReference(raw)
	}
	reference.Fragment = ""
	return label, reference.String(), nil
}

func mediaWikiFileName(raw string) string {
	value := strings.TrimSpace(stdhtml.UnescapeString(raw))
	if hasFilePrefix(value) {
		return cleanFileLabel(value)
	}
	u, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if title := u.Query().Get("title"); hasFilePrefix(title) {
		return cleanFileLabel(title)
	}
	decoded, err := url.PathUnescape(u.Path)
	if err != nil {
		decoded = u.Path
	}
	for _, segment := range strings.Split(decoded, "/") {
		if hasFilePrefix(segment) {
			return cleanFileLabel(segment)
		}
	}
	return ""
}

func isBareFileName(raw string) bool {
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Path != "" && !strings.Contains(u.Path, "/") && looksAudioReference(u.Path)
}

func fileNameFromReference(raw string) string {
	if fileName := mediaWikiFileName(raw); fileName != "" {
		return fileName
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	name, err := url.PathUnescape(pathpkg.Base(u.Path))
	if err != nil {
		name = pathpkg.Base(u.Path)
	}
	return cleanFileLabel(name)
}

func anchorFileLabel(node *xhtml.Node, raw string) string {
	anchor := bestAudioAnchor(node, raw)
	if anchor == nil {
		return ""
	}
	return audioAnchorLabel(anchor)
}

// bestAudioAnchor 兼容真实 BWIKI 表格：逻辑文件名链接常是 data-file 节点的前一个兄弟，
// 而 data-file 本身保存的是 CDN 哈希地址。优先匹配同 URL，其次选择 DOM 中最近的链接。
func bestAudioAnchor(node *xhtml.Node, raw string) *xhtml.Node {
	if node == nil {
		return nil
	}
	var local []*xhtml.Node
	walkNodes(node, func(current *xhtml.Node) {
		if current.Type == xhtml.ElementNode && current.Data == "a" && audioAnchorLabel(current) != "" {
			local = append(local, current)
		}
	})
	if len(local) > 0 {
		return bestMatchingAnchor(local, node, raw)
	}
	cell := nearestCell(node)
	if cell == nil {
		return nil
	}
	var anchors []*xhtml.Node
	walkNodes(cell, func(current *xhtml.Node) {
		if current.Type == xhtml.ElementNode && current.Data == "a" && audioAnchorLabel(current) != "" {
			anchors = append(anchors, current)
		}
	})
	return bestMatchingAnchor(anchors, node, raw)
}

func bestMatchingAnchor(anchors []*xhtml.Node, node *xhtml.Node, raw string) *xhtml.Node {
	if len(anchors) == 0 {
		return nil
	}
	cell := nearestCell(node)
	if cell == nil {
		cell = node
	}
	order := make(map[*xhtml.Node]int)
	position := 0
	walkNodes(cell, func(current *xhtml.Node) {
		order[current] = position
		position++
	})
	nodePosition := order[node]
	raw = strings.TrimSpace(stdhtml.UnescapeString(raw))
	rawName := fileNameFromReference(raw)
	bestScore := -1 << 30
	var best *xhtml.Node
	for _, anchor := range anchors {
		href, _ := attribute(anchor, "href")
		score := 0
		if strings.TrimSpace(stdhtml.UnescapeString(href)) == raw {
			score += 10000
		} else if rawName != "" && strings.EqualFold(fileNameFromReference(href), rawName) {
			score += 5000
		}
		distance := order[anchor] - nodePosition
		if distance <= 0 {
			score += 1000 + distance
		} else {
			score += 500 - distance
		}
		if score > bestScore {
			bestScore = score
			best = anchor
		}
	}
	return best
}

func audioAnchorLabel(anchor *xhtml.Node) string {
	if anchor == nil {
		return ""
	}
	if title, ok := attribute(anchor, "title"); ok {
		title = cleanFileLabel(title)
		if looksAudioReference(title) {
			return title
		}
	}
	label := cleanFileLabel(nodeText(anchor))
	if looksAudioReference(label) {
		return label
	}
	return ""
}

func anchorClaimedByDataFile(anchor *xhtml.Node, successful map[*xhtml.Node]bool) bool {
	cell := nearestCell(anchor)
	if cell == nil {
		return false
	}
	claimed := false
	walkNodes(cell, func(current *xhtml.Node) {
		if claimed || current.Type != xhtml.ElementNode {
			return
		}
		raw, ok := attribute(current, "data-file")
		if ok && successful[current] && bestAudioAnchor(current, raw) == anchor {
			claimed = true
		}
	})
	return claimed
}

func cleanFileLabel(value string) string {
	value = cleanTextValue(value)
	prefixes := []string{"媒体文件:", "媒体文件：", "文件:", "文件：", "media:", "file:"}
	for {
		changed := false
		lower := strings.ToLower(value)
		for _, prefix := range prefixes {
			if strings.HasPrefix(lower, strings.ToLower(prefix)) {
				value = strings.TrimSpace(value[len(prefix):])
				changed = true
				break
			}
		}
		if !changed {
			return value
		}
	}
}

func hasFilePrefix(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{"媒体文件:", "媒体文件：", "文件:", "文件：", "media:", "file:"} {
		if strings.HasPrefix(value, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func looksAudioReference(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Path != "" {
		value = parsed.Path
	}
	value = strings.ToLower(value)
	for _, extension := range []string{".mp3", ".ogg", ".wav", ".m4a", ".aac", ".flac", ".opus", ".webm"} {
		if strings.HasSuffix(value, extension) {
			return true
		}
	}
	return false
}

func languageFromFileName(fileName string) Language {
	if zhFilePattern.MatchString(fileName) {
		return LanguageZH
	}
	if jaFilePattern.MatchString(fileName) {
		return LanguageJA
	}
	if enFilePattern.MatchString(fileName) {
		return LanguageEN
	}
	return LanguageUnknown
}

func languageFromAttributes(node, stop *xhtml.Node) Language {
	for current := node; current != nil; current = current.Parent {
		for _, name := range []string{"lang", "data-lang", "data-language"} {
			if value, ok := attribute(current, name); ok {
				if language := normalizeLanguage(value); language != LanguageUnknown {
					return language
				}
			}
		}
		if current == stop {
			break
		}
	}
	return LanguageUnknown
}

// languageFromPresentation 兼容 BWIKI 语音表格沿用的中/日/英背景色。
// 颜色只作为文件名、lang 属性均缺失时的末级提示，避免覆盖显式语义。
func languageFromPresentation(node, stop *xhtml.Node) Language {
	for current := node; current != nil; current = current.Parent {
		style, _ := attribute(current, "style")
		style = strings.ToLower(strings.ReplaceAll(style, " ", ""))
		switch {
		case strings.Contains(style, "255,192,203"), strings.Contains(style, "#ffc0cb"):
			return LanguageZH
		case strings.Contains(style, "255,255,224"), strings.Contains(style, "#ffffe0"):
			return LanguageJA
		case strings.Contains(style, "240,255,255"), strings.Contains(style, "#f0ffff"):
			return LanguageEN
		}
		if current == stop {
			break
		}
	}
	return LanguageUnknown
}

func normalizeLanguage(value string) Language {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case value == "cn", value == "zh", strings.HasPrefix(value, "zh-"):
		return LanguageZH
	case value == "jp", value == "ja", strings.HasPrefix(value, "ja-"):
		return LanguageJA
	case value == "en", strings.HasPrefix(value, "en-"):
		return LanguageEN
	default:
		return LanguageUnknown
	}
}

func languageFromText(value string) Language {
	var han, latin int
	for _, character := range value {
		switch {
		case unicode.In(character, unicode.Hiragana, unicode.Katakana):
			return LanguageJA
		case unicode.In(character, unicode.Han):
			han++
		case unicode.Is(unicode.Latin, character):
			latin++
		}
	}
	if han > 0 {
		return LanguageZH
	}
	if latin > 0 {
		return LanguageEN
	}
	return LanguageUnknown
}

func candidateSections(root *xhtml.Node, candidates []*audioCandidate) map[*audioCandidate]string {
	byNode := make(map[*xhtml.Node][]*audioCandidate)
	for _, candidate := range candidates {
		byNode[candidate.node] = append(byNode[candidate.node], candidate)
	}
	result := make(map[*audioCandidate]string)
	section := ""
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if isHeading(node) {
			section = cleanHeading(node)
		}
		for _, candidate := range byNode[node] {
			result[candidate] = section
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	return result
}

func walkSectionElements(root *xhtml.Node, handle func(*xhtml.Node, string)) {
	section := ""
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if isHeading(node) {
			section = cleanHeading(node)
		}
		if node.Type == xhtml.ElementNode && node.Data == "table" {
			handle(node, section)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
}

func cleanHeading(node *xhtml.Node) string {
	return cleanTextValue(nodeTextSkipping(node, func(current *xhtml.Node) bool {
		if current.Type != xhtml.ElementNode {
			return false
		}
		class, _ := attribute(current, "class")
		return strings.Contains(class, "mw-editsection")
	}))
}

func extractDisplayTitle(root *xhtml.Node) string {
	var firstHeading, heading, title string
	walkNodes(root, func(node *xhtml.Node) {
		if node.Type != xhtml.ElementNode {
			return
		}
		if id, _ := attribute(node, "id"); id == "firstHeading" && firstHeading == "" {
			firstHeading = cleanHeading(node)
		}
		if node.Data == "h1" && heading == "" {
			heading = cleanHeading(node)
		}
		if node.Data == "title" && title == "" {
			title = cleanTextValue(nodeText(node))
		}
	})
	if firstHeading != "" {
		return firstHeading
	}
	if heading != "" {
		return heading
	}
	return title
}

func stripHTML(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	document, err := xhtml.Parse(strings.NewReader(value))
	if err != nil {
		return cleanTextValue(value)
	}
	return cleanTextValue(nodeText(document))
}

func tableRows(table *xhtml.Node) []*xhtml.Node {
	var rows []*xhtml.Node
	walkNodes(table, func(node *xhtml.Node) {
		if node != table && node.Type == xhtml.ElementNode && node.Data == "tr" && nearestElement(node, "table") == table {
			rows = append(rows, node)
		}
	})
	return rows
}

func directCells(row *xhtml.Node) []*xhtml.Node {
	var cells []*xhtml.Node
	for child := row.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == xhtml.ElementNode && (child.Data == "td" || child.Data == "th") {
			cells = append(cells, child)
		}
	}
	return cells
}

func filterTableCandidates(candidates []*audioCandidate, table *xhtml.Node) []*audioCandidate {
	result := make([]*audioCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.table == table {
			result = append(result, candidate)
		}
	}
	return result
}

func rowSpan(cell *xhtml.Node) int {
	value, ok := attribute(cell, "rowspan")
	if !ok {
		return 1
	}
	span, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || span < 1 {
		return 1
	}
	return span
}

func nearestCell(node *xhtml.Node) *xhtml.Node {
	for current := node; current != nil; current = current.Parent {
		if current.Type == xhtml.ElementNode && (current.Data == "td" || current.Data == "th") {
			return current
		}
		if current.Type == xhtml.ElementNode && current.Data == "tr" {
			return nil
		}
	}
	return nil
}

func nearestElement(node *xhtml.Node, name string) *xhtml.Node {
	for current := node.Parent; current != nil; current = current.Parent {
		if current.Type == xhtml.ElementNode && current.Data == name {
			return current
		}
	}
	return nil
}

func languageTextContainer(node, row *xhtml.Node) *xhtml.Node {
	for current := node.Parent; current != nil && current != row; current = current.Parent {
		if _, ok := attribute(current, "lang"); ok {
			return current
		}
		if _, ok := attribute(current, "data-lang"); ok {
			return current
		}
		class, _ := attribute(current, "class")
		if strings.Contains(class, "ship_word_media_wrap") || strings.Contains(class, "voice-language") {
			return current
		}
	}
	return nil
}

func genericContainer(node *xhtml.Node) *xhtml.Node {
	for current := node.Parent; current != nil; current = current.Parent {
		if current.Type != xhtml.ElementNode {
			continue
		}
		switch current.Data {
		case "li", "p", "td", "th", "figcaption":
			return current
		case "div":
			text := cleanTextWithoutAudio(current)
			if text != "" && len([]rune(text)) <= 2000 {
				return current
			}
		}
	}
	return node
}

func genericTitle(candidate *audioCandidate, container *xhtml.Node) string {
	for current := candidate.node; current != nil; current = current.Parent {
		for _, name := range []string{"data-title", "data-name", "aria-label", "title"} {
			if value, ok := attribute(current, name); ok {
				value = cleanTextValue(value)
				if value != "" && !looksAudioReference(value) {
					return value
				}
			}
		}
		if current == container {
			break
		}
	}
	return candidate.fileName
}

func cleanTextWithoutAudio(node *xhtml.Node) string {
	if node == nil {
		return ""
	}
	return cleanTextValue(nodeTextSkipping(node, func(current *xhtml.Node) bool {
		if current == node || current.Type != xhtml.ElementNode {
			return false
		}
		// BWIKI 用下划线标记触发条件；它不是台词或字幕内容。
		if current.Data == "u" {
			return true
		}
		if _, ok := attribute(current, "data-file"); ok || current.Data == "audio" || current.Data == "source" {
			return true
		}
		if current.Data == "script" || current.Data == "style" || current.Data == "noscript" {
			return true
		}
		if current.Data == "a" {
			href, _ := attribute(current, "href")
			return looksAudioReference(href) || looksAudioReference(cleanFileLabel(nodeText(current)))
		}
		return false
	}))
}

func nodeText(node *xhtml.Node) string {
	return nodeTextSkipping(node, nil)
}

func nodeTextSkipping(node *xhtml.Node, skip func(*xhtml.Node) bool) string {
	var builder strings.Builder
	var visit func(*xhtml.Node)
	visit = func(current *xhtml.Node) {
		if skip != nil && skip(current) {
			return
		}
		if current.Type == xhtml.TextNode {
			builder.WriteString(current.Data)
			return
		}
		boundary := isTextBoundary(current)
		if boundary {
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
		if boundary {
			builder.WriteByte(' ')
		}
	}
	if node != nil {
		visit(node)
	}
	return builder.String()
}

func isTextBoundary(node *xhtml.Node) bool {
	if node == nil || node.Type != xhtml.ElementNode {
		return false
	}
	switch node.Data {
	case "br", "p", "div", "li", "tr", "td", "th", "section", "article", "figcaption":
		return true
	default:
		return isHeading(node)
	}
}

func cleanTextValue(value string) string {
	return strings.Join(strings.Fields(stdhtml.UnescapeString(value)), " ")
}

func isNoiseText(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "" || value == "播放" || value == "语音" || value == "音频" || value == "play"
}

func cellContainsCandidate(cell *xhtml.Node, candidates []*audioCandidate) bool {
	for _, candidate := range candidates {
		if candidate.cell == cell {
			return true
		}
	}
	return false
}

func hasSuccessfulAncestor(node *xhtml.Node, successful map[*xhtml.Node]bool) bool {
	for current := node.Parent; current != nil; current = current.Parent {
		if successful[current] {
			return true
		}
	}
	return false
}

func hasAudioClass(node *xhtml.Node) bool {
	class, _ := attribute(node, "class")
	class = strings.ToLower(class)
	return strings.Contains(class, "media-audio") || strings.Contains(class, "audio-player") || strings.Contains(class, "audioplayer")
}

func isHeading(node *xhtml.Node) bool {
	if node.Type != xhtml.ElementNode || len(node.Data) != 2 || node.Data[0] != 'h' {
		return false
	}
	return node.Data[1] >= '1' && node.Data[1] <= '6'
}

func attribute(node *xhtml.Node, name string) (string, bool) {
	if node == nil {
		return "", false
	}
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val, true
		}
	}
	return "", false
}

func walkNodes(root *xhtml.Node, visit func(*xhtml.Node)) {
	if root == nil {
		return
	}
	visit(root)
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		walkNodes(child, visit)
	}
}
