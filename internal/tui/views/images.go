package views

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	tea "github.com/charmbracelet/bubbletea"
)

// ImagesModel lists local images.
type ImagesModel struct {
	all     []docker.ImageInfo
	visible []docker.ImageInfo
	list    listState
	filter  filterBox
	width   int
	height  int
	client  *docker.Client
}

// NewImagesModel creates the images view.
func NewImagesModel(client *docker.Client) ImagesModel {
	return ImagesModel{client: client, filter: newFilterBox()}
}

// SetImages updates the image list.
func (m *ImagesModel) SetImages(imgs []docker.ImageInfo) {
	m.all = imgs
	m.apply()
}

// SetSize sets the render dimensions.
func (m *ImagesModel) SetSize(w, h int) { m.width, m.height = w, h }

// Capturing reports whether the view is consuming all key input.
func (m ImagesModel) Capturing() bool { return m.filter.active }

func imageKey(i docker.ImageInfo) string { return i.FullID + "|" + i.Repo + ":" + i.Tag }

func (m *ImagesModel) apply() {
	m.visible = nil
	for _, i := range m.all {
		if m.filter.match(i.Repo+":"+i.Tag, i.ID) {
			m.visible = append(m.visible, i)
		}
	}
	m.list.sync(m.keys())
}

func (m ImagesModel) keys() []string {
	k := make([]string, len(m.visible))
	for i, img := range m.visible {
		k[i] = imageKey(img)
	}
	return k
}

func (m ImagesModel) Update(msg tea.Msg) (ImagesModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.filter.update(km) {
		m.apply()
		return m, nil
	}
	k := km.String()
	if m.list.handleNav(k, m.keys(), tableRows(m.height-2)) {
		m.list.window(len(m.visible), tableRows(m.height-2))
		return m, nil
	}
	client := m.client

	switch k {
	case "n":
		return m, emit(PromptRequestMsg{
			Title:       "Pull image",
			Placeholder: "e.g. nginx:alpine",
			OnSubmit: func(s string) tea.Cmd {
				s = strings.TrimSpace(s)
				if s == "" {
					return nil
				}
				return task("pull "+s, "pull", s)
			},
		})
	case "P":
		return m, confirm("Prune images", "Remove all dangling (untagged) images?", true, tracked("prune:images", "pruning", "dangling images", func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			n, space, err := client.PruneImages(ctx, false)
			return FlashMsg{Text: fmt.Sprintf("pruned %d images (%s freed)", n, formatBytes(space)), Err: err}
		}))
	case "A":
		return m, confirm("Prune ALL unused images", "Remove every image not used by any container?", true, tracked("prune:images", "pruning", "unused images", func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			n, space, err := client.PruneImages(ctx, true)
			return FlashMsg{Text: fmt.Sprintf("pruned %d images (%s freed)", n, formatBytes(space)), Err: err}
		}))
	}

	if m.list.cursor >= len(m.visible) {
		return m, nil
	}
	img := m.visible[m.list.cursor]
	ref := img.Ref()
	switch k {
	case "d", "D":
		force := k == "D"
		body := fmt.Sprintf("Remove image %s?", ref)
		var others []string
		for _, o := range m.all {
			if o.FullID == img.FullID && o.Ref() != ref {
				others = append(others, o.Ref())
			}
		}
		if len(others) > 0 {
			body += "\nOnly this tag is removed; the image stays because it is also tagged " + strings.Join(others, ", ") + "."
		} else if img.Containers > 0 {
			body += fmt.Sprintf("\nIt is used by %d container(s).", img.Containers)
			if !force {
				body += " Use D to force."
			}
		}
		title := "Remove image"
		if force {
			title = "Force-remove image"
		}
		return m, confirm(title, body, true, op("img:"+ref, "removing", ref, "remove image "+ref, "", 5*time.Minute, func(ctx context.Context) error {
			return client.RemoveImage(ctx, ref, force)
		}))
	case "p":
		if img.Dangling() {
			return m, emit(FlashMsg{Text: "pull", Err: fmt.Errorf("image has no tag to pull")})
		}
		return m, task("pull "+ref, "pull", ref)
	case "i", "enter":
		return m, InspectCmd("image", img.FullID, ref)
	}
	return m, nil
}

func (m ImagesModel) View() string {
	var total int64
	for _, i := range m.all {
		total += i.Size
	}
	right := m.filter.view()
	if right == "" {
		right = styles.DimStyle.Render("total " + formatBytes(uint64(total)))
	}
	title := titleLine(fmt.Sprintf("Images (%d)", len(m.all)), right, m.width)
	help := helpLine(m.width, "n pull new · p re-pull · d remove · D force remove · P prune dangling · A prune unused · i inspect · / filter")

	if len(m.visible) == 0 {
		return title + "\n" + styles.SubtitleStyle.Render("\n  No images.") + "\n" + help
	}

	cols := []Column{
		{Title: "REPOSITORY", Width: 20, Flex: 4},
		{Title: "TAG", Width: 10, Flex: 1},
		{Title: "IMAGE ID", Width: 12},
		{Title: "SIZE", Width: 8},
		{Title: "CREATED", Width: 10},
		{Title: "USED BY", Width: 7},
	}
	rows := make([][]string, len(m.visible))
	for i, img := range m.visible {
		used := styles.DimStyle.Render("—")
		if img.Containers > 0 {
			used = styles.SuccessStyle.Render(fmt.Sprintf("%d", img.Containers))
		}
		repo := img.Repo
		if img.Dangling() {
			repo = styles.DimStyle.Render(repo)
		}
		rows[i] = []string{repo, img.Tag, img.ID, formatBytes(uint64(img.Size)), humanSince(img.CreatedAt), used}
	}
	return title + "\n" + renderTable(cols, rows, &m.list, m.width, m.height-2) + "\n" + help
}
