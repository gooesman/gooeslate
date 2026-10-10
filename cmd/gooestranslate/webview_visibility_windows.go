package main

func (w *webWindow) syncVisibility() {
	if w == nil || !w.ready || w.browser == nil {
		return
	}
	if call(user, "IsWindowVisible", w.hwnd) == 0 || call(user, "IsIconic", w.hwnd) != 0 {
		w.browser.Hide()
		return
	}
	w.browser.Show()
	w.browser.Resize()
	w.browser.NotifyParentWindowPositionChanged()
}
