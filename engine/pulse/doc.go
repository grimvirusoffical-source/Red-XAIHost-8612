// Package pulse implements RedX phase 2, part 2: the .red-xai-db-pulse format,
// live rules that run when records are written.
//
// A pulse file is a Box document in which every top-level box is one rule:
//
//	{PlayerDeath[ON="players.red-xai.db:Player.Health"]} = [
//	    {When[]}      = "<= 0",
//	    {SetTarget[]} = "Player.IsDead = TRUE",
//	    {Timer[]}     = [ {Name[]} = "Respawn", {Seconds[]} = 10,
//	                      {Do[]} = ;["set Health = 100", "set IsDead = FALSE", "emit player.respawned"]: ],
//	    {Emit[]}      = "player.died"
//	];
//
// See docs/phase2.md for the full syntax. The Engine evaluates rules on
// every write that goes through it, runs actions (set field, start/cancel
// timer, emit event), and publishes events to subscribers. Chains of rules
// triggering each other are cut off by loop protection.
package pulse
