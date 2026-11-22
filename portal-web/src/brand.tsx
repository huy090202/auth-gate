import { useEffect } from 'react';
import { Link } from 'react-router';
import { useStrings } from './lib/i18n';

/** The mark: a gate arch with a leaf growing through it. Same drawing as public/favicon.svg. */
export function LogoMark({ size = 28 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true" focusable="false">
      <rect width="32" height="32" rx="8" fill="#0b7041" />
      <path d="M9 26V14.5a7 7 0 0 1 14 0V26" fill="none" stroke="#ecfdf3" strokeWidth="2.6" strokeLinecap="round" />
      <path d="M16 24.5c-3.6-2.6-4.4-7-1.4-10.6 .5-.6 .9-1 1.4-1.4 .5 .4 .9 .8 1.4 1.4 3 3.6 2.2 8-1.4 10.6Z" fill="#6ee0a0" />
      <path d="M16 24.5V16" stroke="#0b7041" strokeWidth="1.2" strokeLinecap="round" />
    </svg>
  );
}

export function Brand() {
  const t = useStrings();
  return (
    <Link to="/" className="brand" aria-label={t.brandHome}>
      <LogoMark />
      <span className="word">AuthGate<span>Portal</span></span>
    </Link>
  );
}

/** Fixed page background: blurred leaf shapes over the body color, plus grain. */
export function Backdrop() {
  return (
    <div className="backdrop" aria-hidden="true">
      <svg className="leaves" viewBox="0 0 1200 800" preserveAspectRatio="xMidYMid slice">
        <path fill="var(--leaf-1)" d="M-40 120C120 -20 380 20 420 200 450 340 300 420 160 400 20 380 -140 260 -40 120Z" />
        <path fill="var(--leaf-2)" d="M1240 80C1100 -10 880 40 860 210 840 370 1000 440 1120 400 1250 360 1330 160 1240 80Z" />
        <path fill="var(--leaf-3)" d="M700 820C620 680 700 540 860 560 1010 580 1060 720 980 800 900 880 760 900 700 820Z" />
      </svg>
      <div className="grain" />
    </div>
  );
}

/** Sets document.title while mounted. */
export function useTitle(title: string) {
  useEffect(() => {
    document.title = title;
  }, [title]);
}

export const portalTitle = (screen: string) => `${screen} · AuthGate Portal`;
