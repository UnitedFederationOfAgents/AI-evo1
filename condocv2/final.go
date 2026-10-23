package main

// The final states. Both are markers only: the pointer's phase moves to
// archived or abandoned, recording the phase it left (finalFrom) and when
// (finalAt), in one pointer commit. Nothing else is touched -- no slopspace,
// branch, PR or condoc file -- and condocv2 never drives a final condoc
// again. The UI hides final condocs unless "show final" is on.
//
//	archive  -- after merge: the end of the normal lifecycle
//	abandon  -- from any non-final phase, forcibly (the UI asks first)

import (
	"fmt"
	"time"
)

// slopArchive puts a merged condoc away.
func (s *Server) slopArchive(stem string) error {
	return s.finalize(stem, PhaseArchived, "archive", func(spec *PointerSpec) error {
		if spec.Phase != PhaseMerged {
			return fmt.Errorf("only a merged condoc can be archived; %s is %s", spec.Name, spec.Phase)
		}
		return nil
	})
}

// slopAbandon ends a condoc wherever it is. A running turn holds the claim,
// so Abandon is refused (errBusy) until it finishes; a queued one is dropped
// (and runTurn skips a final condoc should the tick have queued it again).
func (s *Server) slopAbandon(stem string) error {
	return s.finalize(stem, PhaseAbandoned, "abandon", func(*PointerSpec) error {
		s.dequeue(stem)
		return nil
	})
}

func (s *Server) finalize(stem, phase, kind string, check func(*PointerSpec) error) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if isFinalPhase(spec.Phase) {
		return fmt.Errorf("%s is already %s", spec.Name, spec.Phase)
	}
	if err := check(&spec); err != nil {
		return err
	}
	spec.FinalFrom, spec.FinalAt = spec.Phase, time.Now().UTC().Format(time.RFC3339)
	spec.Phase = phase
	return s.writeSpec(&spec, commitMsg(&spec, kind, 0))
}
