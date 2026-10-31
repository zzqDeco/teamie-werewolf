package game

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zzqDeco/teamie-werewolf/internal/auth"
)

func makeTestClaims(idx int) auth.Claims {
	return auth.Claims{
		UserID:         fmt.Sprintf("user-%02d", idx),
		DisplayName:    fmt.Sprintf("Player%d", idx),
		WorkspaceID:    "ws-test",
		ConversationID: "conv-test",
		AppID:          "app-test",
		Slug:           "werewolf",
	}
}

func fastTimings() Timings {
	return Timings{
		NightTimeout:       5 * time.Second,
		DayAnnounceDelay:   0, // instant transition when no Hunter shot pending
		HunterTimeout:      5 * time.Second,
		SpeakerTurnTimeout: 5 * time.Second,
		VoteTimeout:        5 * time.Second,
	}
}

func TestZeroDirtySideEffectsOnGetView(t *testing.T) {
	mgr := NewManager(fastTimings())
	defer mgr.CloseAll()

	probeClaims := auth.Claims{
		UserID:         "00000000-0000-4000-8000-000000000001",
		DisplayName:    "Teamie Protocol Verifier",
		WorkspaceID:    "00000000-0000-4000-8000-000000000002",
		ConversationID: "00000000-0000-4000-8000-000000000003",
	}
	view := mgr.GetView(probeClaims)
	if view.Phase != PhaseLobby || view.Me.Seated {
		t.Fatalf("expected unseated LOBBY view, got %+v", view)
	}
	if mgr.RoomCount() != 0 {
		t.Fatalf("expected 0 allocated rooms after read-only GetView, got %d", mgr.RoomCount())
	}
}

func TestSixPlayerFogOfWarAndFullGameFlow(t *testing.T) {
	var mu sync.Mutex
	var capturedNotifs []TargetedNotification

	mgr := NewManager(fastTimings())
	defer mgr.CloseAll()
	mgr.SetCallbacks(func(ns []TargetedNotification) {
		mu.Lock()
		capturedNotifs = append(capturedNotifs, ns...)
		mu.Unlock()
	}, nil)

	// Deterministic 6p deck:
	// Seat 1: WEREWOLF, Seat 2: WEREWOLF, Seat 3: VILLAGER, Seat 4: VILLAGER, Seat 5: SEER, Seat 6: WITCH
	mgr.SetShuffleFunc(func(deck []Role) {
		fixed := []Role{RoleWerewolf, RoleWerewolf, RoleVillager, RoleVillager, RoleSeer, RoleWitch}
		copy(deck, fixed)
	})

	players := make([]auth.Claims, 6)
	room := mgr.GetOrCreateRoom("ws-test", "conv-test")
	for i := 0; i < 6; i++ {
		players[i] = makeTestClaims(i + 1)
		if _, err := room.JoinLobby(players[i], "6p", fmt.Sprintf("join-%d", i+1)); err != nil {
			t.Fatalf("JoinLobby %d failed: %v", i+1, err)
		}
	}

	// Idempotent start_game test
	startResp1, err := room.StartGame(players[0], "6p", 30, "idem-start-1")
	if err != nil {
		t.Fatalf("StartGame failed: %v", err)
	}
	if startResp1.View.Phase != PhaseNight || startResp1.PublicChatPolicy.Allowed {
		t.Fatalf("expected NIGHT with Allowed=false, got phase=%s policy=%+v", startResp1.View.Phase, startResp1.View.PublicChatPolicy)
	}
	startResp2, err := room.StartGame(players[0], "6p", 30, "idem-start-1")
	if err != nil || !startResp2.IdempotentReplay {
		t.Fatalf("expected idempotent replay on duplicate start_game key, resp=%+v err=%v", startResp2, err)
	}

	// Wolf 1 sends private dark chat
	if _, err := room.WolfChat(players[0], "今晚先刀3号平民位", "wolf-chat-1"); err != nil {
		t.Fatalf("WolfChat failed: %v", err)
	}
	// Villager trying WolfChat MUST be rejected
	if _, err := room.WolfChat(players[2], "我是好人偷听", "villager-wolf-chat"); err == nil {
		t.Fatal("expected Villager WolfChat to be rejected")
	}

	// Assert Fog-of-War during NIGHT for Villager (Seat 3) & Seer (Seat 5) & Spectator
	for _, observer := range []auth.Claims{players[2], players[4], makeTestClaims(99)} {
		v := room.GetView(observer)
		rawJSON, _ := json.Marshal(v)
		rawStr := string(rawJSON)
		if strings.Contains(rawStr, "今晚先刀3号平民位") {
			t.Fatalf("CRITICAL FOG LEAK: wolf_chat leaked to %s: %s", observer.UserID, rawStr)
		}
		if strings.Contains(rawStr, "antidoteLeft") || strings.Contains(rawStr, "poisonLeft") {
			t.Fatalf("CRITICAL FOG LEAK: witch potions leaked to %s: %s", observer.UserID, rawStr)
		}
		for _, seat := range v.Seats {
			if seat.IsMe {
				continue
			}
			if seat.KnownRole != "" {
				t.Fatalf("CRITICAL FOG LEAK: seat %d role %s leaked to %s", seat.Seat, seat.KnownRole, observer.UserID)
			}
		}
	}

	// Wolf 2 (Seat 2) MUST see Wolf 1's wolf_chat and teammate seat
	wolf2View := room.GetView(players[1])
	if wolf2View.Private == nil || len(wolf2View.Private.WolfChat) != 1 || wolf2View.Seats[0].KnownRole != RoleWerewolf {
		t.Fatalf("expected Wolf 2 to see wolf_chat and Wolf 1 role, got %+v", wolf2View)
	}

	// Seer (Seat 5) checks Seat 1 (WEREWOLF)
	seerResp, err := room.CastNightAction(players[4], "check", 1, "seer-n1")
	if err != nil {
		t.Fatalf("Seer check failed: %v", err)
	}
	if len(seerResp.View.Private.SeerChecks) != 1 || seerResp.View.Private.SeerChecks[0].Alignment != AlignmentWolf {
		t.Fatalf("expected Seer check alignment WOLF on Seat 1, got %+v", seerResp.View.Private)
	}

	// Wolves (Seat 1 & Seat 2) vote to kill Seat 3
	if _, err := room.CastNightAction(players[0], "kill", 3, "w1-n1"); err != nil {
		t.Fatalf("Wolf 1 kill failed: %v", err)
	}
	if _, err := room.CastNightAction(players[1], "kill", 3, "w2-n1"); err != nil {
		t.Fatalf("Wolf 2 kill failed: %v", err)
	}

	// Witch (Seat 6) should now see TonightAttackedSeat == 3 (since AntidoteLeft == true), while Villager/Seer do NOT
	witchView := room.GetView(players[5])
	if witchView.Private == nil || witchView.Private.WitchState == nil || witchView.Private.WitchState.TonightAttackedSeat == nil || *witchView.Private.WitchState.TonightAttackedSeat != 3 {
		t.Fatalf("expected Witch to see TonightAttackedSeat=3, got %+v", witchView.Private)
	}
	// Witch saves Seat 3!
	dayResp, err := room.CastNightAction(players[5], "save", 0, "witch-n1")
	if err != nil {
		t.Fatalf("Witch save failed: %v", err)
	}

	// Night 1 resolves immediately -> peaceful night -> DAY_SPEECH (since DayAnnounceDelay == 0)
	if dayResp.View.Phase != PhaseDaySpeech {
		t.Fatalf("expected DAY_SPEECH after Night 1 resolution, got %s", dayResp.View.Phase)
	}

	// Strict single-speaker mic order in DAY_SPEECH: Seat 1 speaks first
	if dayResp.View.Speech == nil || dayResp.View.Speech.CurrentSpeakerSeat != 1 {
		t.Fatalf("expected Seat 1 as first speaker, got %+v", dayResp.View.Speech)
	}
	// Seat 2 trying to finish_speech out of turn MUST fail
	if _, err := room.FinishSpeech(players[1], "", "out-of-turn"); err == nil {
		t.Fatal("expected out-of-turn finish_speech to fail")
	}
	// Seat 1 has PublicChatPolicy.Allowed == true, Seat 2 has Allowed == false
	if !room.GetView(players[0]).PublicChatPolicy.Allowed {
		t.Fatal("expected active speaker Seat 1 to have PublicChatPolicy.Allowed=true")
	}
	if room.GetView(players[1]).PublicChatPolicy.Allowed {
		t.Fatal("expected non-active speaker Seat 2 to have PublicChatPolicy.Allowed=false")
	}

	// All 6 alive players finish speech in turn 1..6
	for i := 0; i < 6; i++ {
		if _, err := room.FinishSpeech(players[i], fmt.Sprintf("P%d陈词", i+1), fmt.Sprintf("speech-%d", i+1)); err != nil {
			t.Fatalf("FinishSpeech seat %d failed: %v", i+1, err)
		}
	}

	// Now in DAY_VOTE! Verify secret ballots BEFORE all votes are cast
	vCheck := room.GetView(players[2])
	if vCheck.Phase != PhaseDayVote {
		t.Fatalf("expected DAY_VOTE, got %s", vCheck.Phase)
	}
	// Seat 1 votes for Seat 5, Seat 5 votes for Seat 1
	if _, err := room.SubmitVote(players[0], 5, "v-1"); err != nil {
		t.Fatalf("SubmitVote 1 failed: %v", err)
	}
	if _, err := room.SubmitVote(players[4], 1, "v-5"); err != nil {
		t.Fatalf("SubmitVote 5 failed: %v", err)
	}
	// Seat 3 checks view BEFORE tally: MUST NOT see that Seat 1 voted for 5 or Seat 5 voted for 1!
	midVoteView := room.GetView(players[2])
	midRaw, _ := json.Marshal(midVoteView)
	if len(midVoteView.VoteHistory) != 0 || midVoteView.Private != nil {
		t.Fatalf("CRITICAL FOG LEAK: unrevealed votes leaked before tally: %s", string(midRaw))
	}

	// Remaining players (Seats 2, 3, 4, 6) vote to exile Wolf 1 (Seat 1)
	_, _ = room.SubmitVote(players[1], 5, "v-2")
	_, _ = room.SubmitVote(players[2], 1, "v-3")
	_, _ = room.SubmitVote(players[3], 1, "v-4")
	tallyResp, err := room.SubmitVote(players[5], 1, "v-6")
	if err != nil {
		t.Fatalf("final SubmitVote failed: %v", err)
	}

	// Wolf 1 exiled -> Round 2 NIGHT! Dead Seat 1's role MUST STILL BE HIDDEN from Villagers before GAME_OVER!
	if tallyResp.View.Phase != PhaseNight || tallyResp.View.Round != 2 {
		t.Fatalf("expected Round 2 NIGHT after exiling Wolf 1, got phase=%s round=%d", tallyResp.View.Phase, tallyResp.View.Round)
	}
	villagerR2 := room.GetView(players[2])
	if villagerR2.Seats[0].Alive {
		t.Fatal("expected Seat 1 to be dead")
	}
	if villagerR2.Seats[0].KnownRole != "" {
		t.Fatalf("CRITICAL FOG LEAK: dead player's role revealed before GAME_OVER: %s", villagerR2.Seats[0].KnownRole)
	}

	// Night 2: Wolf 2 attacks Seat 3; Witch has NO antidote left (so TonightAttackedSeat MUST be nil!) and poisons Wolf 2 (Seat 2)!
	_, _ = room.CastNightAction(players[4], "check", 2, "seer-n2")
	_, _ = room.CastNightAction(players[1], "kill", 3, "w2-n2")
	witchN2 := room.GetView(players[5])
	if witchN2.Private.WitchState.TonightAttackedSeat != nil {
		t.Fatalf("expected Witch without antidote NOT to see TonightAttackedSeat, got %+v", witchN2.Private.WitchState)
	}
	endResp, err := room.CastNightAction(players[5], "poison", 2, "witch-poison-w2")
	if err != nil {
		t.Fatalf("Witch poison failed: %v", err)
	}

	// Wolf 2 dies -> 0 wolves left -> GOOD wins -> GAME_OVER, and all roles are now revealed!
	if endResp.View.Phase != PhaseGameOver || endResp.View.Winner != AlignmentGood {
		t.Fatalf("expected GAME_OVER with GOOD winner, got phase=%s winner=%s", endResp.View.Phase, endResp.View.Winner)
	}
	finalVillagerView := room.GetView(players[2])
	for _, s := range finalVillagerView.Seats {
		if s.KnownRole == "" {
			t.Fatalf("expected all roles revealed at GAME_OVER, seat %d empty", s.Seat)
		}
	}

	// Verify every emitted MCP notification payload is < 2KB and carries no cross-user secrets
	mu.Lock()
	defer mu.Unlock()
	if len(capturedNotifs) == 0 {
		t.Fatal("expected targeted notifications to be emitted")
	}
	for _, n := range capturedNotifs {
		raw, _ := json.Marshal(n.Payload)
		if len(raw) >= 2048 {
			t.Fatalf("notification exceeded 2KB limit (%d bytes): %s", len(raw), string(raw))
		}
		if n.Payload.EventType == "wolf_chat_message" && n.TargetUserID != players[1].UserID {
			t.Fatalf("wolf_chat notification sent to non-wolf %s", n.TargetUserID)
		}
	}
}

func TestNinePlayerHunterPoisonedVsWolfKilledRules(t *testing.T) {
	mgr := NewManager(fastTimings())
	defer mgr.CloseAll()

	// 9p deck:
	// 1,2,3: WEREWOLF; 4,5,6: VILLAGER; 7: SEER; 8: WITCH; 9: HUNTER
	fixedDeck := []Role{
		RoleWerewolf, RoleWerewolf, RoleWerewolf,
		RoleVillager, RoleVillager, RoleVillager,
		RoleSeer, RoleWitch, RoleHunter,
	}

	// Subtest A: Hunter killed by wolves at night -> CAN shoot in DAY_ANNOUNCE
	roomA := mgr.GetOrCreateRoom("ws-9p", "conv-hunter-wolfkill")
	roomA.SetShuffleFunc(func(d []Role) { copy(d, fixedDeck) })
	p := make([]auth.Claims, 9)
	for i := 0; i < 9; i++ {
		p[i] = auth.Claims{
			UserID:         fmt.Sprintf("u9-%d", i+1),
			DisplayName:    fmt.Sprintf("P%d", i+1),
			WorkspaceID:    "ws-9p",
			ConversationID: "conv-hunter-wolfkill",
		}
		_, _ = roomA.JoinLobby(p[i], "9p", "")
	}
	_, err := roomA.StartGame(p[0], "9p", 30, "")
	if err != nil {
		t.Fatalf("9p StartGame failed: %v", err)
	}
	_, _ = roomA.CastNightAction(p[6], "check", 1, "")
	_, _ = roomA.CastNightAction(p[0], "kill", 9, "")
	_, _ = roomA.CastNightAction(p[1], "kill", 9, "")
	_, _ = roomA.CastNightAction(p[2], "kill", 9, "")
	res, err := roomA.CastNightAction(p[7], "pass", 0, "")
	if err != nil {
		t.Fatalf("Witch pass failed: %v", err)
	}
	if res.View.Phase != PhaseDayAnnounce {
		t.Fatalf("expected DAY_ANNOUNCE waiting for Hunter shot, got %s", res.View.Phase)
	}
	hunterView := roomA.GetView(p[8])
	if hunterView.Me.PendingAction != "hunter_shoot" || hunterView.Private == nil || !hunterView.Private.HunterCanShoot {
		t.Fatalf("expected Hunter to have hunter_shoot pending, got %+v", hunterView)
	}
	shootRes, err := roomA.HunterShoot(p[8], 1, "hunter-shot-1")
	if err != nil {
		t.Fatalf("HunterShoot failed: %v", err)
	}
	if shootRes.View.Phase != PhaseDaySpeech || shootRes.View.Seats[0].Alive {
		t.Fatalf("expected Seat 1 dead and transition to DAY_SPEECH, got phase=%s seat1Alive=%v", shootRes.View.Phase, shootRes.View.Seats[0].Alive)
	}

	// Subtest B: Hunter poisoned by Witch at night -> CANNOT shoot!
	roomB := mgr.GetOrCreateRoom("ws-9p", "conv-hunter-poisoned")
	roomB.SetShuffleFunc(func(d []Role) { copy(d, fixedDeck) })
	pB := make([]auth.Claims, 9)
	for i := 0; i < 9; i++ {
		pB[i] = auth.Claims{
			UserID:         fmt.Sprintf("ub-%d", i+1),
			DisplayName:    fmt.Sprintf("PB%d", i+1),
			WorkspaceID:    "ws-9p",
			ConversationID: "conv-hunter-poisoned",
		}
		_, _ = roomB.JoinLobby(pB[i], "9p", "")
	}
	_, _ = roomB.StartGame(pB[0], "9p", 30, "")
	_, _ = roomB.CastNightAction(pB[6], "check", 1, "")
	_, _ = roomB.CastNightAction(pB[0], "kill", 4, "")
	_, _ = roomB.CastNightAction(pB[1], "kill", 4, "")
	_, _ = roomB.CastNightAction(pB[2], "kill", 4, "")
	resB, err := roomB.CastNightAction(pB[7], "poison", 9, "")
	if err != nil {
		t.Fatalf("Witch poison Hunter failed: %v", err)
	}
	// Must skip Hunter gun and go straight to DAY_SPEECH!
	if resB.View.Phase != PhaseDaySpeech {
		t.Fatalf("expected poisoned Hunter NOT to trigger gun (straight to DAY_SPEECH), got %s", resB.View.Phase)
	}
	if _, err := roomB.HunterShoot(pB[8], 1, ""); err == nil {
		t.Fatal("expected poisoned Hunter HunterShoot call to be rejected")
	}
}

func TestPhaseTimeoutAutoAdvance(t *testing.T) {
	mgr := NewManager(Timings{
		NightTimeout:       60 * time.Millisecond,
		DayAnnounceDelay:   20 * time.Millisecond,
		HunterTimeout:      60 * time.Millisecond,
		SpeakerTurnTimeout: 40 * time.Millisecond,
		VoteTimeout:        60 * time.Millisecond,
	})
	defer mgr.CloseAll()

	room := mgr.GetOrCreateRoom("ws-timeout", "conv-timeout")
	for i := 0; i < 6; i++ {
		_, _ = room.JoinLobby(makeTestClaims(i+1), "6p", "")
	}
	// Do NOT pass custom phaseTimeoutSec so our millisecond Timings remain active
	_, err := room.StartGame(makeTestClaims(1), "6p", 0, "")
	if err != nil {
		t.Fatalf("StartGame failed: %v", err)
	}

	// Wait for NIGHT timeout (60ms) + DAY_ANNOUNCE delay (20ms) -> DAY_SPEECH
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if room.GetView(makeTestClaims(1)).Phase == PhaseDaySpeech {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got := room.GetView(makeTestClaims(1)).Phase; got != PhaseDaySpeech {
		t.Fatalf("expected auto-advance to DAY_SPEECH after NightTimeout, got %s", got)
	}

	// Wait for 6 speaker timeouts (6 * 40ms) -> DAY_VOTE -> VoteTimeout (60ms) -> Round 2 NIGHT
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v := room.GetView(makeTestClaims(1))
		if v.Phase == PhaseNight && v.Round == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	v := room.GetView(makeTestClaims(1))
	if v.Phase != PhaseNight || v.Round != 2 {
		t.Fatalf("expected auto-advance through DAY_SPEECH and DAY_VOTE into Round 2 NIGHT, got phase=%s round=%d", v.Phase, v.Round)
	}
}

func TestWolfDisagreementRevisionWitchSubTimerAndPostGameLeave(t *testing.T) {
	mgr := NewManager(Timings{
		NightTimeout:       80 * time.Millisecond,
		WitchTimeout:       450 * time.Millisecond,
		DayAnnounceDelay:   0,
		HunterTimeout:      200 * time.Millisecond,
		SpeakerTurnTimeout: 200 * time.Millisecond,
		VoteTimeout:        200 * time.Millisecond,
	})
	defer mgr.CloseAll()

	// Seat 1,2: WEREWOLF; Seat 3,4: VILLAGER; Seat 5: SEER; Seat 6: WITCH
	mgr.SetShuffleFunc(func(deck []Role) {
		copy(deck, []Role{RoleWerewolf, RoleWerewolf, RoleVillager, RoleVillager, RoleSeer, RoleWitch})
	})

	room := mgr.GetOrCreateRoom("ws-bugs", "conv-bugs")
	p := make([]auth.Claims, 6)
	for i := 0; i < 6; i++ {
		p[i] = makeTestClaims(i + 1)
		_, _ = room.JoinLobby(p[i], "6p", "")
	}
	if _, err := room.StartGame(p[0], "6p", 0, ""); err != nil {
		t.Fatalf("StartGame failed: %v", err)
	}

	// Seer checks Seat 1
	_, _ = room.CastNightAction(p[4], "check", 1, "")

	// Wolf 1 votes Seat 3, Wolf 2 votes Seat 4 (disagreement!)
	if _, err := room.CastNightAction(p[0], "kill", 3, ""); err != nil {
		t.Fatalf("Wolf 1 vote failed: %v", err)
	}
	rDisagree, err := room.CastNightAction(p[1], "kill", 4, "")
	if err != nil {
		t.Fatalf("Wolf 2 disagreeing vote failed: %v", err)
	}
	if !strings.Contains(rDisagree.Message, "分歧") {
		t.Fatalf("expected disagreement hint in message, got %q", rDisagree.Message)
	}

	// Because wolves disagreed, nightStep MUST still be WOLVES_AND_SEER (WolfCanVote=true, Witch CanActNow=false)
	w1View := room.GetView(p[0])
	if w1View.Private == nil || !w1View.Private.WolfCanVote {
		t.Fatalf("expected Wolf 1 WolfCanVote=true when wolves disagree, got %+v", w1View.Private)
	}
	witchEarly := room.GetView(p[5])
	if witchEarly.Private == nil || witchEarly.Private.WitchState == nil || witchEarly.Private.WitchState.CanActNow {
		t.Fatalf("expected Witch NOT active before wolves agree, got %+v", witchEarly.Private)
	}

	// Wait 55ms (most of the 80ms NightTimeout), then Wolf 1 revises vote to Seat 4 -> unanimous lock!
	time.Sleep(55 * time.Millisecond)
	rAgree, err := room.CastNightAction(p[0], "kill", 4, "")
	if err != nil {
		t.Fatalf("Wolf 1 vote revision failed: %v", err)
	}
	if !strings.Contains(rAgree.Message, "锁定") {
		t.Fatalf("expected lock confirmation on unanimous wolf vote, got %q", rAgree.Message)
	}
	// Further wolf revision after unanimous lock must fail with wolf_target_locked
	if _, lockErr := room.CastNightAction(p[1], "kill", 3, ""); ErrorCodeOf(lockErr) != "wolf_target_locked" {
		t.Fatalf("expected wolf_target_locked after consensus, got err=%v code=%s", lockErr, ErrorCodeOf(lockErr))
	}

	// Sleep another 70ms (total 125ms > original 80ms NightTimeout!):
	// Witch MUST STILL be in NIGHT with CanActNow=true thanks to dedicated 450ms WitchTimeout!
	time.Sleep(70 * time.Millisecond)
	witchActive := room.GetView(p[5])
	if witchActive.Phase != PhaseNight || !witchActive.Private.WitchState.CanActNow {
		t.Fatalf("Witch was starved by shared timer! phase=%s witchState=%+v", witchActive.Phase, witchActive.Private)
	}

	// Witch poisons Wolf 1 (Seat 1) -> Night 1 casualties: Seat 4 (Villager) + Seat 1 (Wolf) -> DAY_SPEECH
	if _, err := room.CastNightAction(p[5], "poison", 1, ""); err != nil {
		t.Fatalf("Witch poison failed: %v", err)
	}
	// Alive seats in DAY_SPEECH: 2, 3, 5, 6 -> finish speeches and vote out Wolf 2 (Seat 2) -> GAME_OVER!
	for step := 0; step < 4; step++ {
		cur := room.GetView(p[1]).Speech.CurrentSpeakerSeat
		if _, err := room.FinishSpeech(p[cur-1], "简短陈词", ""); err != nil {
			t.Fatalf("FinishSpeech seat %d failed: %v", cur, err)
		}
	}
	for _, voterIdx := range []int{1, 2, 4, 5} {
		target := 2
		if voterIdx == 1 {
			target = 5
		}
		_, _ = room.SubmitVote(p[voterIdx], target, "")
	}
	if got := room.GetView(p[0]).Phase; got != PhaseGameOver {
		t.Fatalf("expected GAME_OVER after exiling Wolf 2, got %s", got)
	}

	// LeaveLobby in GAME_OVER: Players 1, 2, 3 leave -> Seated=false for them, but Player 4 still sees GAME_OVER scoreboard!
	for _, idx := range []int{0, 1, 2} {
		lr, lErr := room.LeaveLobby(p[idx], "")
		if lErr != nil {
			t.Fatalf("LeaveLobby in GAME_OVER failed for player %d: %v", idx+1, lErr)
		}
		if lr.View.Me.Seated {
			t.Fatalf("expected player %d Seated=false after LeaveLobby in GAME_OVER", idx+1)
		}
	}
	p4View := room.GetView(p[3])
	if p4View.Phase != PhaseGameOver || p4View.Seats[0].KnownRole != RoleWerewolf {
		t.Fatalf("expected GAME_OVER recap preserved while others remain, got phase=%s", p4View.Phase)
	}

	// Remaining players 4, 5, 6 leave -> room resets to clean 0/6 LOBBY
	for _, idx := range []int{3, 4, 5} {
		if _, lErr := room.LeaveLobby(p[idx], ""); lErr != nil {
			t.Fatalf("LeaveLobby in GAME_OVER failed for player %d: %v", idx+1, lErr)
		}
	}
	cleanView := room.GetView(p[0])
	if cleanView.Phase != PhaseLobby || len(cleanView.Seats) != 0 {
		t.Fatalf("expected clean 0/6 LOBBY after all players left GAME_OVER, got phase=%s seats=%d", cleanView.Phase, len(cleanView.Seats))
	}
}
