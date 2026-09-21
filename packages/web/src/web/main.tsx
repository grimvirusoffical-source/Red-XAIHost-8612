// Entry point referenced by index.html — composition only, the real bootstrap
// lives in __main.tsx (template-managed).
//
// The returning managed sign-in leg is finished inside lib/auth.ts with a
// top-level await, which suspends this module graph until the session is known,
// so __main's first paint never flashes the sign-in screen at a signed-in owner.
import "./__main";
