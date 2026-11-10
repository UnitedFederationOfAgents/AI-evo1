package main

// The final states. Both are markers only: the pointer's phase moves to
// archived or abandoned, recording the phase it left (finalFrom) and when
// (finalAt), in one pointer commit. Nothing else is touched -- no slopspace,
// branch, PR or condoc file -- and condocv2 doesn't drive a final condoc
// (unless it is unabandoned). The UI hides final condocs unless "show final"
// is on.
//
//	archive    -- after merge: the end of the normal lifecycle
//	abandon    -- from any phase before merge, forcibly (the UI asks first)
//	unabandon  -- undoes abandon: back to the phase it left

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

// slopAbandon ends a condoc that hasn't merged yet (a merged one is archived
// instead). A running turn holds the claim, so Abandon is refused (errBusy)
// until it finishes; a queued one is dropped (and runTurn skips a final
// condoc should the tick have queued it again).
func (s *Server) slopAbandon(stem string) error {
	return s.finalize(stem, PhaseAbandoned, "abandon", func(spec *PointerSpec) error {
		if spec.Phase == PhaseMerged {
			return fmt.Errorf("%s is merged; archive it instead", spec.Name)
		}
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

// slopUnabandon puts an abandoned condoc back in the phase it left and clears
// the marker, in one pointer commit. Abandon touched nothing else, so the
// lock, error and slopspace are as they were and the tick picks up where it
// stopped (re-queuing a dropped turn).
func (s *Server) slopUnabandon(stem string) error {
	if !s.claim(stem) {
		return errBusy(stem)
	}
	defer s.release(stem)
	spec, err := s.readHeldSpec(stem)
	if err != nil {
		return err
	}
	if spec.Phase != PhaseAbandoned {
		return fmt.Errorf("only an abandoned condoc can be unabandoned; %s is %s", spec.Name, spec.Phase)
	}
	if spec.FinalFrom == "" || spec.FinalFrom == PhaseMerged || isFinalPhase(spec.FinalFrom) {
		return fmt.Errorf("%s has no phase to go back to (finalFrom %q)", spec.Name, spec.FinalFrom)
	}
	spec.Phase, spec.FinalFrom, spec.FinalAt = spec.FinalFrom, "", ""
	return s.writeSpec(&spec, commitMsg(&spec, "unabandon", 0))
}
