package note

import (
	"context"
	"fmt"
)

// SyncWorkspace exchanges one selected pad in a fixed host-then-join order.
// The ordered exchange avoids both peers blocking while sending large snapshots.
func (s *Session) SyncWorkspace(ctx context.Context, workspace *Workspace, pad string) ([]Document, error) {
	if s == nil || !s.WorkspaceSyncSupported() {
		return nil, nil
	}
	if workspace == nil {
		workspace = NewWorkspace()
	}
	pad = NormalizePad(pad)
	if err := ValidatePad(pad); err != nil {
		return nil, err
	}

	var applied []Document
	if s.Host() {
		if err := s.sendWorkspaceSnapshot(ctx, workspace.Snapshot(pad)); err != nil {
			return nil, err
		}
		document, changed, err := s.receiveWorkspaceSnapshot(ctx, workspace, pad)
		if err != nil {
			return nil, err
		}
		if changed {
			applied = append(applied, document)
		}
		return applied, nil
	}

	document, changed, err := s.receiveWorkspaceSnapshot(ctx, workspace, pad)
	if err != nil {
		return nil, err
	}
	if changed {
		applied = append(applied, document)
	}
	if err := s.sendWorkspaceSnapshot(ctx, workspace.Snapshot(pad)); err != nil {
		return nil, err
	}
	return applied, nil
}

func (s *Session) sendWorkspaceSnapshot(ctx context.Context, document Document) error {
	var frame Frame
	if document.Revision == 0 {
		frame = Frame{Type: FramePing, Version: ProtocolVersion, Pad: document.Pad}
	} else {
		frameType := FrameUpdate
		if document.Text == "" {
			frameType = FrameClear
		}
		frame = FrameFromDocument(frameType, document)
	}
	if err := s.Send(ctx, frame); err != nil {
		return err
	}
	response, err := s.Recv(ctx)
	if err != nil {
		return err
	}
	if NormalizePad(response.Pad) != NormalizePad(document.Pad) {
		return fmt.Errorf("peer selected note pad %q, local pad is %q", response.Pad, document.Pad)
	}
	if document.Revision == 0 {
		if response.Type != FramePong {
			return fmt.Errorf("expected note workspace pong, got %q", response.Type)
		}
		return nil
	}
	if response.Type != FrameAck {
		return fmt.Errorf("expected note workspace ack, got %q", response.Type)
	}
	return nil
}

func (s *Session) receiveWorkspaceSnapshot(
	ctx context.Context,
	workspace *Workspace,
	pad string,
) (Document, bool, error) {
	frame, err := s.Recv(ctx)
	if err != nil {
		return Document{}, false, err
	}
	if NormalizePad(frame.Pad) != pad {
		return Document{}, false, fmt.Errorf("peer selected note pad %q, local pad is %q", frame.Pad, pad)
	}
	switch frame.Type {
	case FramePing:
		return Document{}, false, s.Send(ctx, Frame{
			Type:    FramePong,
			Version: ProtocolVersion,
			Pad:     pad,
		})
	case FrameUpdate, FrameClear:
		changed, current, err := workspace.ApplyRemote(frame.Document())
		if err != nil {
			return Document{}, false, err
		}
		if err := s.Send(ctx, Frame{
			Type:      FrameAck,
			Version:   ProtocolVersion,
			Pad:       pad,
			Revision:  current.Revision,
			Timestamp: current.Timestamp,
		}); err != nil {
			return Document{}, false, err
		}
		return current, changed, nil
	default:
		return Document{}, false, fmt.Errorf("expected note workspace snapshot, got %q", frame.Type)
	}
}
