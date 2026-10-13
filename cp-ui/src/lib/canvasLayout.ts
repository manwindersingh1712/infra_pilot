// A new service is created at (0, 0). When another service already sits at the
// same position the new node would hide behind it, so the canvas asks the API
// to lay the project out. Otherwise nobody's positions are touched.
export interface Positioned {
  id: string;
  positionX: number;
  positionY: number;
}

export function newServiceOverlaps(services: Positioned[], newId: string): boolean {
  const n = services.find((s) => s.id === newId);
  if (!n) return false;
  return services.some((s) => s.id !== newId && s.positionX === n.positionX && s.positionY === n.positionY);
}
